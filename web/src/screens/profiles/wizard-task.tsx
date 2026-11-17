import type { KeyboardEvent } from "react";
import { cx } from "@/lib/cx";
import type { Tx } from "@/screens/users/t";
import { Gl, Rad } from "./wizard-bits";
import { appNames, type Task, type TaskId } from "./wizard-model";

/** Step 1, "What is this profile for?": the tasks as cards of one radio group (the board's `.pw-tasks`). Arrows move the choice. */
export function TaskStep({ tasks, value, onValue, t }: { tasks: Task[]; value: TaskId; onValue: (id: TaskId) => void; t: Tx }) {
  // arrows choose, as in any radio group: the card that is chosen is the one that can be tabbed to
  const move = (e: KeyboardEvent, i: number) => {
    const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const next = tasks[(i + step + tasks.length) % tasks.length]!;
    onValue(next.id);
    (e.currentTarget.parentElement?.children[(i + step + tasks.length) % tasks.length] as HTMLElement | undefined)?.focus();
  };
  return (
    <>
      <h2 className="pw-q">{t("wiz.q1")}</h2>
      <div className="pw-tasks" role="radiogroup" aria-label={t("wiz.tasks")}>
        {tasks.map((k, i) => {
          const on = k.id === value;
          return (
            <button key={k.id} type="button" role="radio" aria-checked={on} tabIndex={on ? 0 : -1} className={cx("pw-task", `pw-c-${k.id}`)} onClick={() => onValue(k.id)} onKeyDown={(e) => move(e, i)}>
              <span className="pw-tt">
                <span className={cx("pw-g", k.tone)} aria-hidden="true">
                  <Gl d={k.glyph} />
                </span>
                <Rad />
              </span>
              <b>{t(`wiz.t.${k.id}.t`)}</b>
              <span className="pw-tp">{t(`wiz.t.${k.id}.p`)}</span>
              <span className="pw-tw">{t(`wiz.t.${k.id}.w`)}</span>
              <span className="pw-tf">
                <span className="pw-apps">
                  {[...k.apps].map((l) => (
                    <i key={l} title={appNames[l]}>
                      {l === "R" ? "T" : l}
                    </i>
                  ))}
                </span>
                {k.mark && <em className={k.mark.tone}>{t(k.mark.key)}</em>}
              </span>
            </button>
          );
        })}
      </div>
    </>
  );
}
