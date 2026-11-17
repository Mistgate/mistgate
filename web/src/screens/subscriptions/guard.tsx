import { createContext, useContext, useEffect } from "react";

/**
 * A tab with changes that are not saved yet tells the screen so: leaving it asks "Leave without saving?" first (the board's
 * dialog of the Mihomo templates). Only the tab on screen is mounted, so one flag is enough.
 */
export const LeaveGuard = createContext<(dirty: boolean) => void>(() => {});

export function useLeaveGuard(dirty: boolean) {
  const set = useContext(LeaveGuard);
  useEffect(() => {
    set(dirty);
    return () => set(false);
  }, [dirty, set]);
}
