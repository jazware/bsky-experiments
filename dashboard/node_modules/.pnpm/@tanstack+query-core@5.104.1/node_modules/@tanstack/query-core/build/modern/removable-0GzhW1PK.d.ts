//#region src/removable.d.ts
/**
 * The base class for cache entries that are garbage collected once nothing is using them —
 * `Query` and `Mutation` both extend it. `gcTime` controls how long an unused entry is kept.
 */
declare abstract class Removable {
  #private;
  gcTime: number;
  /**
   * Clears the pending garbage collection timeout, so the entry is no longer scheduled for removal.
   * A subclass may override this to release what it holds on to as well — `Query` also cancels any
   * in-flight fetch.
   */
  destroy(): void;
  protected scheduleGc(): void;
  protected updateGcTime(newGcTime: number | undefined): void;
  protected clearGcTimeout(): void;
  protected abstract optionalRemove(): void;
}
//#endregion
export { Removable as t };
//# sourceMappingURL=removable-0GzhW1PK.d.ts.map