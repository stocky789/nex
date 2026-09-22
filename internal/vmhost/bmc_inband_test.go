// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// inBandLab is one fake management controller reached over a fake /dev/ipmi0. It answers the
// ipmitool subcommands this host uses, records every one of them, and refuses anything it was
// told to refuse, so every branch is exercised without a machine.
type inBandLab struct {
	users   map[uint8]string // slot -> name, "" for a free slot
	lan     string           // what "lan print" reports, "" for a channel with none
	ciphers string
	refuse  map[string]bool // subcommand prefix -> refuse
	ran     []string
	stdin   []string
}

func newInBandLab(t *testing.T, lab *inBandLab) *inBandLab {
	t.Helper()
	device := filepath.Join(t.TempDir(), "ipmi0")
	if err := os.WriteFile(device, nil, 0600); err != nil {
		t.Fatal(err)
	}
	previousDevice, previousRun := ipmiDevice, storageFenceEnvRun
	ipmiDevice = device
	fakeCommand(t, "ipmitool", "exit 0")
	storageFenceEnvRun = func(_ context.Context, _ []string, stdin, name string, args ...string) ([]byte, error) {
		if name != "ipmitool" {
			t.Fatalf("something other than ipmitool ran in-band: %s", name)
		}
		line := strings.Join(args, " ")
		lab.ran = append(lab.ran, line)
		if stdin != "" {
			lab.stdin = append(lab.stdin, stdin)
		}
		for prefix := range lab.refuse {
			if strings.Contains(line, prefix) {
				return nil, os.ErrPermission
			}
		}
		switch {
		case strings.HasPrefix(line, "-I open lan print"):
			// Only channel 1 is a LAN channel on this controller.
			if !strings.HasSuffix(line, " 1") || lab.lan == "" {
				return nil, os.ErrNotExist
			}
			return []byte("Set in Progress         : Set Complete\nIP Address Source       : Static Address\nIP Address              : " + lab.lan +
				"\nRMCP+ Cipher Suites     : " + lab.ciphers + "\n"), nil
		case strings.HasPrefix(line, "-I open user list"):
			// Verbatim from ipmitool 1.8.19: the header string and the row format
			// "%-4d%-17s%-8s%-11s%-11s%-s" are the ones in the shipped binary (both
			// read out of /usr/bin/ipmitool with strings; ipmi_user.c prints them
			// unconditionally, and -c csv does not change them). The header's tabs are
			// why a parser must not derive the name column from it.
			out := "ID  Name\t     Callin  Link Auth\tIPMI Msg   Channel Priv Limit\n"
			for id := uint8(1); id <= 4; id++ {
				name, ok := lab.users[id]
				if !ok {
					continue
				}
				out += fmt.Sprintf("%-4d%-17s%-8s%-11s%-11s%-s\n", id, name, "true", "true", "true", "ADMINISTRATOR")
			}
			return []byte(out), nil
		case strings.HasPrefix(line, "-I open user set name"):
			lab.users[uint8(args[len(args)-2][0]-'0')] = args[len(args)-1]
		}
		return nil, nil
	}
	t.Cleanup(func() { ipmiDevice, storageFenceEnvRun = previousDevice, previousRun })
	return lab
}

func defaultInBandLab(t *testing.T) *inBandLab {
	return newInBandLab(t, &inBandLab{
		users:   map[uint8]string{1: "", 2: "admin", 3: "", 4: ""},
		lan:     "192.0.2.15",
		ciphers: "0,1,2,3,6,7,8,11,12,17",
		refuse:  map[string]bool{},
	})
}

// provision runs both halves the way the agent does: plan, then configure.
func provision(t *testing.T, known LocalBMCAccount) (LocalBMCResult, string, error) {
	t.Helper()
	plan := PlanLocalBMCAccount(context.Background(), known)
	if plan.State != "" {
		return plan, "", errors.New(plan.Detail)
	}
	password, err := ConfigureLocalBMCAccount(context.Background(), plan)
	return plan, password, err
}

func TestCoreConfiguresItsOwnManagementControllerInBand(t *testing.T) {
	lab := defaultInBandLab(t)
	result, password, err := provision(t, LocalBMCAccount{})
	if err != nil {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	if result.Address != "192.0.2.15" || result.Channel != 1 || result.Account != BMCFenceAccount {
		t.Fatalf("discovery = %#v", result)
	}
	// The first free slot, never the one that already holds an account.
	if result.Slot != 3 {
		t.Fatalf("slot = %d, which is not the first free one", result.Slot)
	}
	// Suite 17 where the controller offers it; suite 0 is never chosen.
	if result.Cipher != 17 {
		t.Fatalf("cipher = %d", result.Cipher)
	}
	if len(password) != 16 {
		t.Fatalf("password length = %d", len(password))
	}
	ran := strings.Join(lab.ran, "\n")
	// The password reaches the controller on standard input and never as an argument: a
	// command line is readable by every process on this host while it runs.
	if strings.Contains(ran, password) {
		t.Fatal("the management controller password was passed as a command argument")
	}
	if len(lab.stdin) != 1 || lab.stdin[0] != password+"\n"+password+"\n" {
		t.Fatalf("the password was not written to ipmitool's own prompt: %q", lab.stdin)
	}
	// Operator, the lowest privilege that may issue Chassis Control. Administrator would
	// give a fence account the run of the controller for nothing.
	if !strings.Contains(ran, "user priv 3 3 1") || strings.Contains(ran, "privilege=4") {
		t.Fatalf("the fence account was not limited to power control: %s", ran)
	}
	if !strings.Contains(ran, "channel setaccess 1 3 callin=off ipmi=on link=on privilege=3") {
		t.Fatalf("IPMI over LAN was not enabled for the fence account: %s", ran)
	}
}

// The account an operator already made under that name is not Nex's to take over.
func TestAForeignAccountUnderTheNexNameIsAConflictAndNothingIsChanged(t *testing.T) {
	lab := newInBandLab(t, &inBandLab{users: map[uint8]string{2: BMCFenceAccount, 3: ""}, lan: "192.0.2.15", ciphers: "3", refuse: map[string]bool{}})
	result := PlanLocalBMCAccount(context.Background(), LocalBMCAccount{})
	if result.State != protocol.PoolBMCNameConflict {
		t.Fatalf("result = %#v", result)
	}
	for _, line := range lab.ran {
		if strings.Contains(line, "user set") || strings.Contains(line, "user enable") {
			t.Fatalf("an account Nex Core did not create was changed: %s", line)
		}
	}
	// The same controller, once this host knows the account is its own, is provisioned in
	// place — no second account.
	result, _, err := provision(t, LocalBMCAccount{Slot: 2, Created: true})
	if err != nil || result.Slot != 2 {
		t.Fatalf("Core did not reuse the account it created: %#v (%v)", result, err)
	}
}

func TestInBandProvisioningRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lab   *inBandLab
		state string
	}{
		{"no free user slot", &inBandLab{users: map[uint8]string{2: "admin", 3: "backup", 4: "vendor"}, lan: "192.0.2.15", ciphers: "3"}, protocol.PoolBMCUserRefused},
		{"in-band user management refused", &inBandLab{users: map[uint8]string{2: ""}, lan: "192.0.2.15", ciphers: "3", refuse: map[string]bool{"user list": true}}, protocol.PoolBMCUserRefused},
		{"the account cannot be given power control", &inBandLab{users: map[uint8]string{2: ""}, lan: "192.0.2.15", ciphers: "3", refuse: map[string]bool{"user priv": true}}, protocol.PoolBMCUserRefused},
		{"IPMI over LAN cannot be enabled", &inBandLab{users: map[uint8]string{2: ""}, lan: "192.0.2.15", ciphers: "3", refuse: map[string]bool{"channel setaccess": true}}, protocol.PoolBMCUserRefused},
		{"no LAN address on any channel", &inBandLab{users: map[uint8]string{2: ""}, ciphers: "3"}, protocol.PoolBMCLANDisabled},
		{"no authenticated cipher suite", &inBandLab{users: map[uint8]string{2: ""}, lan: "192.0.2.15", ciphers: "0"}, protocol.PoolBMCLANDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.lab.refuse == nil {
				tc.lab.refuse = map[string]bool{}
			}
			lab := newInBandLab(t, tc.lab)
			before := map[uint8]string{}
			for slot, name := range tc.lab.users {
				before[slot] = name
			}
			result, password, err := provision(t, LocalBMCAccount{})
			if err == nil {
				t.Fatalf("a controller that refuses provisioned anyway: %#v", result)
			}
			state := result.State
			if state == "" {
				state = protocol.PoolBMCUserRefused // The plan was fine; the write refused.
			}
			if state != tc.state || password != "" || err.Error() == "" {
				t.Fatalf("state = %q, err %v", state, err)
			}
			// What matters is not only the sentence: a refusal that ran after the name
			// was set leaves an account, and the next pass must recognise it as its own
			// and finish rather than refuse to touch it for ever.
			if lab.users[result.Slot] == BMCFenceAccount {
				finished, _, retryErr := provision(t, LocalBMCAccount{Slot: result.Slot, Created: true})
				if retryErr != nil && strings.Contains(retryErr.Error(), "already exists") {
					t.Fatalf("the next pass refused this host's own half-made account: %v", retryErr)
				}
				_ = finished
			} else if result.Slot != 0 && before[result.Slot] != lab.users[result.Slot] {
				t.Fatalf("a refusal left slot %d as %q", result.Slot, lab.users[result.Slot])
			}
		})
	}
}

// Without a local IPMI interface there is nothing to configure, and the manual form is the
// path. This is the common case: a host that is itself a virtual machine.
func TestAHostWithNoLocalIPMIInterfaceSaysSo(t *testing.T) {
	lab := defaultInBandLab(t)
	ipmiDevice = filepath.Join(t.TempDir(), "absent")
	result := PlanLocalBMCAccount(context.Background(), LocalBMCAccount{})
	if result.State != protocol.PoolBMCNoInterface || len(lab.ran) != 0 {
		t.Fatalf("result = %#v, ran = %v", result, lab.ran)
	}
}

// Suite 17 is preferred, suite 3 is the floor, and suite 0 — no authentication at all — is
// never selected, because anyone who could reach the controller could then power the host off.
func TestCipherSuiteSelection(t *testing.T) {
	for _, tc := range []struct {
		offered string
		want    uint8
	}{{"0,1,2,3,6,7,8,11,12,17", 17}, {"0,1,2,3", 3}, {"", 3}} {
		got, err := lanCipherSuite([]byte("IP Address : 192.0.2.15\nRMCP+ Cipher Suites     : " + tc.offered + "\n"))
		if tc.offered == "" {
			got, err = lanCipherSuite([]byte("IP Address : 192.0.2.15\n"))
		}
		if err != nil || got != tc.want {
			t.Fatalf("suites %q gave %d (%v)", tc.offered, got, err)
		}
	}
	if _, err := lanCipherSuite([]byte("RMCP+ Cipher Suites     : 0\n")); err == nil {
		t.Fatal("a controller offering only cipher suite 0 was accepted")
	}
}

func TestRemovingTheFenceAccountDisablesItBeforeItLosesItsName(t *testing.T) {
	lab := defaultInBandLab(t)
	if err := RemoveLocalBMCAccount(context.Background(), 1, 3); err != nil {
		t.Fatal(err)
	}
	ran := strings.Join(lab.ran, "\n")
	disabled, cleared := strings.Index(ran, "user disable 3"), strings.Index(ran, "user set name 3")
	if disabled < 0 || cleared < 0 || cleared < disabled {
		t.Fatalf("the account's name was cleared before it was disabled: %s", ran)
	}
}

// A controller that refuses halfway through — busy, mid-firmware-update, out of resources —
// used to leave a `nexfence` account nothing vouched for, and this host then refused to touch
// its own account for ever. (Reported by independent review with a working demonstration
// against the first implementation of this slice.) The plan half now decides the slot before
// anything is written, so the caller records it first and the next pass finishes the job.
func TestAControllerThatRefusesHalfwayLeavesAnAccountThisHostStillOwns(t *testing.T) {
	lab := newInBandLab(t, &inBandLab{users: map[uint8]string{2: "admin", 3: ""}, lan: "192.0.2.15",
		ciphers: "3", refuse: map[string]bool{"user set password": true}})
	plan := PlanLocalBMCAccount(context.Background(), LocalBMCAccount{})
	if plan.State != "" || plan.Slot != 3 {
		t.Fatalf("plan = %#v", plan)
	}
	// Nothing was written while the slot was being chosen: the caller records it first.
	for _, line := range lab.ran {
		if strings.Contains(line, "user set") || strings.Contains(line, "user enable") {
			t.Fatalf("planning changed the controller: %s", line)
		}
	}
	if _, err := ConfigureLocalBMCAccount(context.Background(), plan); err == nil {
		t.Fatal("a controller that refused the password reported success")
	}
	if lab.users[3] != BMCFenceAccount {
		t.Fatalf("the demonstration no longer strands an account: %v", lab.users)
	}
	// The next pass, with the slot this host recorded before it wrote anything.
	lab.refuse = map[string]bool{}
	retry := PlanLocalBMCAccount(context.Background(), LocalBMCAccount{Slot: 3, Created: true})
	if retry.State != "" || retry.Slot != 3 {
		t.Fatalf("the next pass refused this host's own half-made account: %#v", retry)
	}
	if _, err := ConfigureLocalBMCAccount(context.Background(), retry); err != nil {
		t.Fatalf("the next pass could not finish the account: %v", err)
	}
}

// The user table is read by ipmitool's own row format, so a name that fills its column is read
// whole. The header is printed with tabs and must not be used for offsets.
func TestTheUserTableIsReadByIPMIToolsOwnRowFormat(t *testing.T) {
	lab := newInBandLab(t, &inBandLab{users: map[uint8]string{2: "administrator", 3: ""}, lan: "192.0.2.15",
		ciphers: "3", refuse: map[string]bool{}})
	users, err := ipmiUserList(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if users[2] != "administrator" {
		t.Fatalf("a full-width name was truncated to %q", users[2])
	}
	_ = lab
	// A table Nex Core cannot read is refused outright: reading a slot as free when it is
	// not would hand Nex somebody else's account.
	previous := storageFenceEnvRun
	storageFenceEnvRun = func(context.Context, []string, string, string, ...string) ([]byte, error) {
		return []byte("ID  Name\t     Callin\n2 admin true\n"), nil
	}
	t.Cleanup(func() { storageFenceEnvRun = previous })
	if _, err = ipmiUserList(context.Background(), 1); err == nil {
		t.Fatal("an unrecognised user table was parsed anyway")
	}
}

// A controller that spaces out its cipher list must not be read as offering nothing, and one
// that really offers only suite 0 must still be refused however it prints the list.
func TestCipherListsAreReadWhateverTheSeparator(t *testing.T) {
	got, err := lanCipherSuite([]byte("RMCP+ Cipher Suites     : 0,1,2,3, 17\n"))
	if err != nil || got != 17 {
		t.Fatalf("a spaced list gave %d (%v)", got, err)
	}
	if _, err = lanCipherSuite([]byte("RMCP+ Cipher Suites     : 0, 0\n")); err == nil {
		t.Fatal("a controller offering only cipher suite 0 was accepted")
	}
}
