/**
 * Types for `scripts/buildId.mjs`, which `test/self-update.test.ts` imports to
 * assert the bundle script and the runner hash build files identically.
 */
export declare const BUILD_ID_FILES: readonly string[];
export declare function computeBuildId(files: ReadonlyArray<Buffer | Uint8Array>): string;
