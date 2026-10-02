// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useState } from "react";

// Update channels. Stable follows the main branch; Development follows dev and is a pre-release of the next
// stable release. The channel is a setting Core keeps on each host: the picker only asks the host to change it
// and shows what the host reports.
export const CHANNELS = [{ id: "stable", label: "Stable" }, { id: "dev", label: "Development" }];
export const channelLabel = (id?: string) => CHANNELS.find(c => c.id === id)?.label ?? id ?? "—";

export function ChannelPicker({ current, disabled, apply }: { current?: string; disabled: boolean; apply: (channel: string) => Promise<void> }) {
  const reported = current || "stable";
  // A channel set through the API that this picker does not offer stays selectable, so the page never
  // shows a different value from the one the host reports.
  const options = CHANNELS.some(c => c.id === reported) ? CHANNELS : [...CHANNELS, { id: reported, label: reported }];
  const [choice, setChoice] = useState(reported);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => setChoice(reported), [reported]);
  async function change() {
    setPending(true); setError("");
    try { await apply(choice); } catch (e) { setError((e as Error).message || "The request failed."); } finally { setPending(false); }
  }
  return <div className="channel-picker">
    <label>Update channel
      <select value={choice} disabled={disabled || pending} onChange={e => setChoice(e.target.value)}>
        {options.map(c => <option key={c.id} value={c.id}>{c.label}</option>)}
      </select>
    </label>
    <button type="button" className="sm" disabled={disabled || pending || choice === reported} onClick={() => void change()}>Change channel</button>
    <p className="small">Development builds are pre-releases of the next stable release. Moving back to Stable offers nothing until Stable publishes a newer release than the one this host runs.</p>
    {error && <p className="warning" role="alert">{error}</p>}
  </div>;
}
