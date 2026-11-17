import type { Tx } from "@/screens/users/t";
import { GroupDot } from "@/screens/users/user-bits";
import { Cb } from "./wizard-bits";

/** One group as the step draws it. `id` "" is the group "Everyone" the wizard makes when the panel has none. */
export type GroupVM = { id: string; name: string; color: string; people: number; on: boolean };

/** Step 4, "Who gets it": the groups as ticks with their people, what that adds up to, and how the profile reaches them (the board's `.pw-ppl`). */
export function PeopleStep({ t, name, groups, sum, deliv, notes, onPick }: { t: Tx; name: string; groups: GroupVM[]; sum: number; deliv: string; notes: string[]; onPick: (id: string) => void }) {
  return (
    <>
      <h2 className="pw-q">{t("wiz.q4", { name })}</h2>
      <section className="pw-card pw-sec pw-ppl">
        {groups.map((g) => (
          <button key={g.id || "everyone"} type="button" className="pw-pnh pw-gr" role="checkbox" aria-checked={g.on} onClick={() => onPick(g.id)}>
            <Cb on={g.on} />
            <GroupDot id={g.id || "everyone"} color={g.color} />
            <b>{g.name}</b>
            <em>{t.n("wiz.ppl", g.people)}</em>
          </button>
        ))}
        <p className="pw-quiet pw-b">{sum > 0 ? t.n("wiz.sum", sum) : t("wiz.sumNone")}</p>
        {notes.map((n) => (
          <p key={n} className="pw-q1 pw-pad">
            {n}
          </p>
        ))}
        <p className="pw-q1 pw-pad">{deliv}</p>
      </section>
    </>
  );
}
