import * as React from "react";
//#region src/IsRestoringProvider.d.ts
/**
 * If you are using `PersistQueryClientProvider`, you can also use the `useIsRestoring` hook alongside it to
 * check if a restore is currently in progress. `useQuery` and friends also check this internally to avoid
 * race conditions between the restore and mounting queries.
 *
 * @returns `true` while a persisted client is being restored, `false` otherwise.
 */
export declare const useIsRestoring: () => boolean;
/**
 * The Provider that `PersistQueryClientProvider` uses to signal whether a persisted client is currently
 * being restored, read by `useIsRestoring`.
 */
export declare const IsRestoringProvider: React.Provider<boolean>;
//#endregion
//# sourceMappingURL=IsRestoringProvider.d.cts.map