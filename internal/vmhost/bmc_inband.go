// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// A management controller is part of the machine Nex Core runs on, and Core can talk to it
// from the inside over /dev/ipmi0 without a password, a network or an operator. So it does:
// it finds its own controller's LAN address, creates one dedicated account for fencing with
// a random password and nothing but power control, and reports what it found either way.
//
// Everything here is refusal-first. Core replaces an account under its own name only when it
// created that account; it never takes a slot that holds somebody else's account; and when
// anything is not possible in-band it says which thing, in a sentence an operator can act on,
// rather than leaving a profile that looks usable.

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"nex.local/nex/internal/protocol"
)

// ipmiDevice is the kernel's own interface to this machine's management controller. It is a
// variable so a test can point the whole of this file at one it created.
var ipmiDevice = "/dev/ipmi0"

const (
	// BMCFenceAccount is the one account name Nex owns on a host's own controller.
	BMCFenceAccount = "nexfence"
	// IPMI 2.0 requires Operator privilege for the Chassis Control command, which is the
	// whole of what a fence does. A User-privilege account can read the system GUID and
	// the power state but cannot power the host off, so Operator is the floor here.
	ipmiOperatorPriv  = "3"
	ipmiUserSlotLimit = 15
	// The widths of ipmitool's own "%-4d%-17s..." user-table row.
	ipmiUserIDWidth   = 4
	ipmiUserNameWidth = 17
)

var (
	ipmiLANAddress = regexp.MustCompile(`(?im)^\s*IP Address\s*:\s*(\S+)\s*$`)
	// The rest of the line, because firmware prints the list with commas, with spaces, or
	// with both; anchoring to one token would silently discard a controller's real offer.
	ipmiCipherList = regexp.MustCompile(`(?im)^\s*RMCP\+ Cipher Suites\s*:\s*(.*)$`)
)

// LocalBMCAccount is what this host already knows about the account it uses for fencing:
// which slot it is in, and whether Nex Core is the one that put it there. Core's own record
// and the pool master's membership record are both evidence of that; nothing else is.
type LocalBMCAccount struct {
	Slot    uint8
	Created bool
}

// LocalBMCResult is one in-band pass over this host's own controller. An empty State from
// PlanLocalBMCAccount means the plan is usable and the slot is chosen; every other value is a
// reason there is no fence device, in Core's own words.
type LocalBMCResult struct {
	State   string
	Detail  string
	Address string
	Channel uint8
	// LANChannels is every channel that reported a LAN address, so the account can be told
	// to have no access on the ones it is not fenced over.
	LANChannels []uint8
	Cipher      uint8
	Slot        uint8
	Account     string
}

func bmcResult(state, detail string) LocalBMCResult {
	return LocalBMCResult{State: state, Detail: detail}
}

// ipmiInBand runs one local command against this host's own controller. It is the single
// seam every ipmitool interaction in Nex goes through, so a test drives the whole of this
// file against a fake and a password never reaches a command argument.
func ipmiInBand(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	return storageFenceEnvRun(ctx, nil, stdin, "ipmitool", append([]string{"-I", "open"}, args...)...)
}

// PlanLocalBMCAccount finds the controller and decides which user slot the fence account
// belongs in, and changes nothing. It is deliberately separate from the half that writes:
// its caller records the slot durably first, so a controller that refuses halfway through
// leaves an account this host still recognises as its own rather than one it will refuse to
// touch for ever.
func PlanLocalBMCAccount(ctx context.Context, known LocalBMCAccount) LocalBMCResult {
	if _, err := exec.LookPath("ipmitool"); err != nil {
		return bmcResult(protocol.PoolBMCNoInterface, "this Nex Core has no ipmitool, so it cannot configure a management controller from the inside")
	}
	if _, err := os.Stat(ipmiDevice); err != nil {
		return bmcResult(protocol.PoolBMCNoInterface, "this host has no local IPMI interface, so Nex Core cannot configure its management controller from the inside; set the controller's access by hand instead")
	}
	result, err := discoverLocalBMC(ctx)
	if err != nil {
		return bmcResult(protocol.PoolBMCLANDisabled, err.Error())
	}
	users, err := ipmiUserList(ctx, result.Channel)
	if err != nil {
		result.State, result.Detail = protocol.PoolBMCUserRefused, "this host's management controller refused in-band account management, so Nex Core could not create a fence account on it; set the controller's access by hand instead"
		return result
	}
	slot, err := fenceAccountSlot(users, known)
	if err != nil {
		result.State, result.Detail = protocol.PoolBMCNameConflict, err.Error()
		if !errors.Is(err, errBMCNameTaken) {
			result.State = protocol.PoolBMCUserRefused
		}
		return result
	}
	result.Slot = slot
	return result
}

// ConfigureLocalBMCAccount is the half that writes: it gives the planned slot its name, a
// random password, power control and access to the LAN channel it will be fenced over, and
// returns the password. Its caller has already recorded the slot, so a refusal anywhere in
// here is a state the next pass finishes rather than a stranded account.
func ConfigureLocalBMCAccount(ctx context.Context, plan LocalBMCResult) (string, error) {
	password := bmcPassword()
	if err := configureFenceAccount(ctx, plan, password); err != nil {
		return "", err
	}
	return password, nil
}

// RemoveLocalBMCAccount takes the account off the controller again. It clears the name last:
// a slot with no name is a free slot, so an interrupted removal leaves an account that is
// disabled and unusable rather than one that is nameless and still enabled.
func RemoveLocalBMCAccount(ctx context.Context, channel, slot uint8) error {
	id := strconv.Itoa(int(slot))
	channelID := strconv.Itoa(int(channel))
	if _, err := ipmiInBand(ctx, "", "channel", "setaccess", channelID, id, "callin=off", "ipmi=off", "link=off", "privilege=15"); err != nil {
		return errors.New("this host's management controller refused to withdraw the fence account's access")
	}
	if _, err := ipmiInBand(ctx, "", "user", "disable", id); err != nil {
		return errors.New("this host's management controller refused to disable the fence account")
	}
	if _, err := ipmiInBand(ctx, "", "user", "set", "name", id, ""); err != nil {
		return errors.New("this host's management controller refused to remove the fence account")
	}
	return nil
}

// discoverLocalBMC finds the controller's own LAN channels. IPMI numbers channels per
// implementation rather than by convention, so the only reliable answer is to ask each one
// that may carry LAN. The first that reports an address is the one a fence uses; the others
// are remembered so the fence account can be denied access on them.
func discoverLocalBMC(ctx context.Context) (LocalBMCResult, error) {
	found := LocalBMCResult{Account: BMCFenceAccount}
	for id := uint8(1); id <= 11; id++ {
		out, runErr := ipmiInBand(ctx, "", "lan", "print", strconv.Itoa(int(id)))
		if runErr != nil {
			continue
		}
		match := ipmiLANAddress.FindSubmatch(out)
		if match == nil || string(match[1]) == "0.0.0.0" {
			continue
		}
		found.LANChannels = append(found.LANChannels, id)
		if found.Channel != 0 {
			continue
		}
		cipher, err := lanCipherSuite(out)
		if err != nil {
			return LocalBMCResult{}, err
		}
		found.Channel, found.Address, found.Cipher = id, string(match[1]), cipher
	}
	if found.Channel == 0 {
		return LocalBMCResult{}, errors.New("this host's management controller reports no LAN address on any channel, and Nex Core could not enable one from the inside; give the controller an address, or set its access by hand")
	}
	return found, nil
}

// lanCipherSuite picks the RMCP+ cipher suite a fence will use. Suite 0 asks for no
// authentication, no integrity and no confidentiality — anyone who can reach the controller
// could power the host off — so it is never selected, and a channel that offers nothing else
// is not a channel Nex will fence over. Suite 17 is preferred where the controller offers it;
// suite 3 is what every IPMI 2.0 controller must implement.
func lanCipherSuite(lanPrint []byte) (uint8, error) {
	match := ipmiCipherList.FindSubmatch(lanPrint)
	if match == nil {
		return 3, nil // Firmware that lists nothing still has to implement suite 3.
	}
	offered := map[string]bool{}
	for _, suite := range strings.FieldsFunc(string(match[1]), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		offered[suite] = true
	}
	switch {
	case offered["17"]:
		return 17, nil
	case offered["3"]:
		return 3, nil
	}
	return 0, errors.New("this host's management controller offers no authenticated RMCP+ cipher suite on its LAN channel, so a fence over it could be issued by anyone who can reach it; set the controller's access by hand after enabling cipher suite 3 or 17")
}

var errBMCNameTaken = errors.New("an account named " + BMCFenceAccount + " already exists on this host's management controller and Nex Core did not create it, so nothing was changed; remove or rename that account, or set the controller's access by hand")

// fenceAccountSlot decides which user slot the fence account belongs in. Core's own account
// is reused in place; an account under the same name that Core did not create is a conflict
// and nothing is touched; otherwise the first slot with no name at all is taken. Slot 1 is
// never used: it is the controller's anonymous/null user.
//
// The slot this host recorded as its own is taken back whether or not the name reached the
// controller. A controller that refused halfway through the last pass left the slot named,
// or left it empty, and either way it is this host's to finish.
func fenceAccountSlot(users map[uint8]string, known LocalBMCAccount) (uint8, error) {
	if known.Created && known.Slot != 0 {
		if name, present := users[known.Slot]; present && (name == "" || name == BMCFenceAccount) {
			return known.Slot, nil
		}
	}
	free := uint8(0)
	for id := uint8(2); id <= ipmiUserSlotLimit; id++ {
		name, present := users[id]
		if !present {
			continue
		}
		if name == BMCFenceAccount {
			if known.Created && (known.Slot == 0 || known.Slot == id) {
				return id, nil
			}
			return 0, errBMCNameTaken
		}
		if name == "" && free == 0 {
			free = id
		}
	}
	if free == 0 {
		return 0, errors.New("this host's management controller has no free user slot for a fence account, so nothing was changed; free a slot, or set the controller's access by hand")
	}
	return free, nil
}

// configureFenceAccount gives the slot its name, its password and power control, and nothing
// else. The password is written to ipmitool's own prompt on standard input: as a command
// argument it would be readable by every process on this host for as long as the command ran.
//
// The account is confined to the one LAN channel a fence uses and explicitly denied access on
// every other LAN channel this controller reports. Channels that are not LAN — serial, IPMB —
// are not touched: whether they can be constrained in-band differs by firmware, so the
// hardware walkthrough checks the account's privilege on every channel rather than this
// guessing at commands a controller may not accept.
func configureFenceAccount(ctx context.Context, plan LocalBMCResult, password string) error {
	id := strconv.Itoa(int(plan.Slot))
	channelID := strconv.Itoa(int(plan.Channel))
	if _, err := ipmiInBand(ctx, "", "user", "set", "name", id, BMCFenceAccount); err != nil {
		return errors.New("this host's management controller refused to name a fence account, so Nex Core could not create one; set the controller's access by hand instead")
	}
	if _, err := ipmiInBand(ctx, password+"\n"+password+"\n", "user", "set", "password", id); err != nil {
		return errors.New("this host's management controller refused to set the fence account's password; set the controller's access by hand instead")
	}
	// Operator, not Administrator: the lowest IPMI privilege that may issue Chassis
	// Control, which is the only thing a fence ever asks this account to do.
	if _, err := ipmiInBand(ctx, "", "user", "priv", id, ipmiOperatorPriv, channelID); err != nil {
		return errors.New("this host's management controller refused to give a fence account power control; set the controller's access by hand instead")
	}
	// The channel-level grant is separate from the user's privilege, and IPMI-over-LAN is
	// off for a new account until it is made. Callin stays off: a fence never dials in.
	if _, err := ipmiInBand(ctx, "", "channel", "setaccess", channelID, id, "callin=off", "ipmi=on", "link=on", "privilege="+ipmiOperatorPriv); err != nil {
		return errors.New("this host's management controller refused to enable IPMI over LAN for the fence account, so a fence could never reach it; set the controller's access by hand instead")
	}
	for _, other := range plan.LANChannels {
		if other == plan.Channel {
			continue
		}
		if _, err := ipmiInBand(ctx, "", "channel", "setaccess", strconv.Itoa(int(other)), id, "callin=off", "ipmi=off", "link=off", "privilege=15"); err != nil {
			return errors.New("this host's management controller refused to keep the fence account off its other LAN channels; set the controller's access by hand instead")
		}
	}
	if _, err := ipmiInBand(ctx, "", "user", "enable", id); err != nil {
		return errors.New("this host's management controller refused to enable the fence account; set the controller's access by hand instead")
	}
	return nil
}

// ipmiUserList reads the controller's user table. ipmitool has one print format for a row —
// "%-4d%-17s%-8s%-11s%-11s%-s" — so the slot is the first four columns and the name the next
// seventeen, whatever the header (which it prints with embedded tabs) looks like. Splitting on
// whitespace could not tell an empty name from a missing column, and an empty name is exactly
// what makes a slot free to take.
//
// A line that does not fit that format is not guessed at: the whole table is refused. Reading
// a slot as free when it is not would hand Nex somebody else's account.
func ipmiUserList(ctx context.Context, channel uint8) (map[uint8]string, error) {
	out, err := ipmiInBand(ctx, "", "user", "list", strconv.Itoa(int(channel)))
	if err != nil {
		return nil, err
	}
	users := map[uint8]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "ID") {
			continue // The header, which ipmitool prints with tabs in it.
		}
		id, convErr := strconv.Atoi(strings.TrimSpace(line[:min(ipmiUserIDWidth, len(line))]))
		if convErr != nil || id < 1 || id > ipmiUserSlotLimit || len(line) < ipmiUserIDWidth+ipmiUserNameWidth {
			return nil, errors.New("this host's management controller printed a user table Nex Core does not recognise")
		}
		users[uint8(id)] = strings.TrimSpace(line[ipmiUserIDWidth : ipmiUserIDWidth+ipmiUserNameWidth])
	}
	if len(users) == 0 {
		return nil, errors.New("this host's management controller reported no user table")
	}
	return users, nil
}

// bmcPassword is 16 characters of randomness — the longest password an IPMI 2.0 controller
// is required to store — over the letters and digits every firmware accepts in one.
func bmcPassword() string { return rand.Text()[:16] }
