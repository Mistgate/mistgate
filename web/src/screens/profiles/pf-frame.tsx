import { Dialog } from "@base-ui/react/dialog";
import { Menu } from "@base-ui/react/menu";
import type { ComponentPropsWithoutRef, ReactElement, ReactNode } from "react";
import { useEffectsEnabled } from "@/lib/effects";
import { cx } from "@/lib/cx";
import { useTx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { D } from "./pf-bits";
import "./profiles.css";
import "./profile-page.css";
import "./profile-tabs.css";

// The two things the profile page draws in a portal, as the board draws them: a window (us-dlg on its scrim) and a menu (up-mn). Their root
// carries the page's class (.pf.pf-dr, display: contents), so the page's selectors reach them.

/** A window as the board draws it: the header with its x, the body (.us-db), the footer. `busy` keeps it open (a run is on its way). */
export function DlgFrame({
  open,
  onClose,
  title,
  className,
  children,
  footer,
  busy,
  shot,
}: {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  className?: string;
  children: ReactNode;
  footer: ReactNode;
  busy?: boolean;
  shot?: string;
}) {
  const t = useTx();
  const fx = useEffectsEnabled();
  return (
    <Dialog.Root open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <Dialog.Portal>
        <div className={cx("pf pf-dr", fx && "pf-fx")}>
          <Dialog.Viewport className="an-scrim us-scrim">
            <div className="an-pos us-pos">
              <Dialog.Popup className={cx("us-dlg ug-dlg pp-dlg", className)} data-shot={shot}>
                <header>
                  <Dialog.Title render={<h2 />}>{title}</Dialog.Title>
                  <Dialog.Close className="ic" aria-label={t("pf.dr.close")}>
                    <G d={D.close} size={20} stroke={1.5} />
                  </Dialog.Close>
                </header>
                {children}
                <footer>{footer}</footer>
              </Dialog.Popup>
            </div>
          </Dialog.Viewport>
        </div>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

/** A menu opened by `trigger` (the row's ⋯, "Add to a group…"): the board's .up-mn, 290 wide, or 220 (`narrow`) aligned to the button's start. */
export function PfMenu({ trigger, children, narrow, label, defaultOpen }: { trigger: ReactElement; children: ReactNode; narrow?: boolean; label?: string; defaultOpen?: boolean }) {
  return (
    <Menu.Root defaultOpen={defaultOpen}>
      <Menu.Trigger render={trigger} />
      <Menu.Portal>
        <Menu.Positioner side="bottom" align={narrow ? "start" : "end"} sideOffset={6} className="pf z-50 outline-none">
          <Menu.Popup className={cx("pp-mn", narrow && "pp-mn3")} aria-label={label}>
            {children}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

export function PfMenuItem({ className, tone, ...rest }: Omit<ComponentPropsWithoutRef<typeof Menu.Item>, "className"> & { className?: string; tone?: "risk" | "ask" }) {
  return <Menu.Item className={cx(tone, className)} {...rest} />;
}

export function PfMenuLink({ className, ...rest }: Omit<ComponentPropsWithoutRef<typeof Menu.LinkItem>, "className"> & { className?: string }) {
  return <Menu.LinkItem closeOnClick className={className} {...rest} />;
}
