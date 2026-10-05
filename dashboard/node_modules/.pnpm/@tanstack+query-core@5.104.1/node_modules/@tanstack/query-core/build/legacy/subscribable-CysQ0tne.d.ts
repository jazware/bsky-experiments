//#region src/subscribable.d.ts
/**
 * The base class behind everything in Query that you can subscribe to: `QueryCache`, `MutationCache`,
 * the observers, and the `FocusManager`/`OnlineManager` behind `focusManager` and `onlineManager`.
 * Subclasses decide what a listener receives and when it is called.
 */
declare class Subscribable<TListener extends Function> {
  protected listeners: Set<TListener>;
  constructor();
  /**
   * Registers a listener to be called on every update this object notifies about. Returns a function
   * that removes the listener again — call it to stop listening. The base class never drops a listener
   * on its own, though some subclasses clear all of theirs in `destroy()`.
   * @param listener - Called on each update, with whatever the subclass passes to its subscribers.
   * @example
   * ```ts
   * const unsubscribe = subscribable.subscribe(() => {
   *   // react to the update
   * })
   *
   * unsubscribe()
   * ```
   */
  subscribe(listener: TListener): () => void;
  /**
   * Returns `true` while at least one listener is registered, `false` once they have all unsubscribed.
   */
  hasListeners(): boolean;
  protected onSubscribe(): void;
  protected onUnsubscribe(): void;
}
//#endregion
export { Subscribable as t };
//# sourceMappingURL=subscribable-CysQ0tne.d.ts.map