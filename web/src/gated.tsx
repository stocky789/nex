// SPDX-License-Identifier: AGPL-3.0-only
import "./settings.css";

// Rule 8 of the design guide: a section whose Nex Core phase has not landed keeps its real
// header, actions and columns, disabled, and says in one row what is missing.
export function Gated({ columns, capability, phase }: { columns: string[]; capability: string; phase: number }) {
  return <div className="table-scroll"><table>
    <thead><tr>{columns.map(c => <th key={c}>{c}</th>)}</tr></thead>
    <tbody><tr><td colSpan={columns.length} className="gated">Available with {capability} (Phase {phase}).</td></tr></tbody>
  </table></div>;
}
export const gatedButtons = (labels: string[], capability: string, phase: number) => labels.map((label, i) =>
  <button key={label} type="button" className={i ? "sm" : "sm primary"} disabled title={`Available with ${capability} (Phase ${phase})`}>{label}</button>);
