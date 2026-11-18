import { createContext, useContext } from "react";

/**
 * Development only: what a scene of the dev kit opens the window with (a provider in view, a row asked about, a search typed, the list). The
 * product never provides it, so everywhere it is empty and nothing changes.
 */
export const SceneContext = createContext<ReadonlySet<string>>(new Set());
export const useScene = () => useContext(SceneContext);
