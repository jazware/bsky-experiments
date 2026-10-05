/**
 * Type the tailwind-merge configuration adheres to.
 */
interface Config<ClassGroupIds extends string, ThemeGroupIds extends string> extends ConfigStaticPart, ConfigGroupsPart<ClassGroupIds, ThemeGroupIds> {
}
/**
 * The static part of the tailwind-merge configuration. When merging multiple configurations, the properties of this interface are always overridden.
 */
interface ConfigStaticPart {
    /**
     * Integer indicating size of LRU cache used for memoizing results.
     * - Cache might be up to twice as big as `cacheSize`
     * - No cache is used for values <= 0
     */
    cacheSize: number;
    /**
     * Prefix added to Tailwind-generated classes
     * @see https://tailwindcss.com/docs/configuration#prefix
     */
    prefix?: string;
    /**
     * Allows to customize parsing of individual classes passed to `twMerge`.
     * All classes passed to `twMerge` outside of cache hits are passed to this function before it is determined whether the class is a valid Tailwind CSS class.
     *
     * This is an experimental feature and may introduce breaking changes in any minor version update.
     */
    experimentalParseClassName?(param: ExperimentalParseClassNameParam): ParsedClassName;
}
/**
 * Type of param passed to the `experimentalParseClassName` function.
 *
 * This is an experimental feature and may introduce breaking changes in any minor version update.
 */
interface ExperimentalParseClassNameParam {
    className: string;
    parseClassName(className: string): ParsedClassName;
}
/**
 * Type of the result returned by the `experimentalParseClassName` function.
 *
 * This is an experimental feature and may introduce breaking changes in any minor version update.
 */
interface ParsedClassName {
    /**
     * Whether the class is external and merging logic should be sipped.
     *
     * If this is `true`, the class will be treated as if it wasn't a Tailwind class and will be passed through as is.
     */
    isExternal?: boolean;
    /**
     * Modifiers of the class in the order they appear in the class.
     *
     * @example ['hover', 'dark'] // for `hover:dark:bg-gray-100`
     */
    modifiers: string[];
    /**
     * Whether the class has an `!important` modifier.
     *
     * @example true // for `hover:dark:!bg-gray-100`
     */
    hasImportantModifier: boolean;
    /**
     * Base class without preceding modifiers.
     *
     * @example 'bg-gray-100' // for `hover:dark:bg-gray-100`
     */
    baseClassName: string;
    /**
     * Index position of a possible postfix modifier in the class.
     * If the class has no postfix modifier, this is `undefined`.
     *
     * This property is prefixed with "maybe" because tailwind-merge does not know whether something is a postfix modifier or part of the base class since it's possible to configure Tailwind CSS classes which include a `/` in the base class name.
     *
     * If a `maybePostfixModifierPosition` is present, tailwind-merge first tries to match the `baseClassName` without the possible postfix modifier to a class group. If that fails or the matched class group is configured in `postfixLookupClassGroups`, it tries again with the possible postfix modifier.
     *
     * @example 11 // for `bg-gray-100/50`
     */
    maybePostfixModifierPosition: number | undefined;
}
/**
 * The dynamic part of the tailwind-merge configuration. When merging multiple configurations, the user can choose to either override or extend the properties of this interface.
 */
interface ConfigGroupsPart<ClassGroupIds extends string, ThemeGroupIds extends string> {
    /**
     * Theme scales used in classGroups.
     *
     * The keys are the same as in the Tailwind config but the values are sometimes defined more broadly.
     */
    theme: NoInfer<ThemeObject<ThemeGroupIds>>;
    /**
     * Object with groups of classes.
     *
     * @example
     * {
     *     // Creates group of classes `group`, `of` and `classes`
     *     'group-id': ['group', 'of', 'classes'],
     *     // Creates group of classes `look-at-me-other` and `look-at-me-group`.
     *     'other-group': [{ 'look-at-me': ['other', 'group']}]
     * }
     */
    classGroups: NoInfer<Record<ClassGroupIds, ClassGroup<ThemeGroupIds>>>;
    /**
     * Conflicting classes across groups.
     *
     * The key is the ID of a class group which creates a conflict, values are IDs of class groups which receive a conflict. That means if a class from from the key ID is present, all preceding classes from the values are removed.
     *
     * A class group ID is the key of a class group in the classGroups object.
     *
     * @example { gap: ['gap-x', 'gap-y'] }
     */
    conflictingClassGroups: NoInfer<Partial<Record<ClassGroupIds, readonly ClassGroupIds[]>>>;
    /**
     * Postfix modifiers conflicting with other class groups.
     *
     * A class group ID is the key of a class group in classGroups object.
     *
     * @example { 'font-size': ['leading'] }
     */
    conflictingClassGroupModifiers: NoInfer<Partial<Record<ClassGroupIds, readonly ClassGroupIds[]>>>;
    /**
     * Class group IDs which should be resolved again with their postfix modifier attached.
     *
     * This is needed when a slash can make the full class name belong to a different class group than the part before the slash.
     *
     * @example ['container-type'] // `@container-size/sidebar` should resolve differently from `@container-size`
     */
    postfixLookupClassGroups?: readonly NoInferString<ClassGroupIds>[];
    /**
     * Modifiers whose order among multiple modifiers should be preserved because their order changes which element gets targeted.
     *
     * tailwind-merge makes sure that classes with these modifiers are not overwritten by classes with the same modifiers with order-sensitive modifiers being in a different position.
     */
    orderSensitiveModifiers: string[];
}
type ThemeObject<ThemeGroupIds extends string> = Record<ThemeGroupIds, ClassGroup<ThemeGroupIds>>;
type ClassGroup<ThemeGroupIds extends string> = readonly ClassDefinition<ThemeGroupIds>[];
type ClassDefinition<ThemeGroupIds extends string> = string | ClassValidator | ThemeGetter | ClassObject<ThemeGroupIds>;
type ClassValidator = (classPart: string) => boolean;
interface ThemeGetter {
    (theme: ThemeObject<AnyThemeGroupIds>): ClassGroup<AnyClassGroupIds>;
    isThemeGetter: true;
    /**
     * The theme key the getter reads. Set on getters created with `fromTheme` so that tooling (e.g. config generators) can identify the referenced theme scale without invoking the getter. Optional because theme getters can be hand-written.
     */
    themeKey?: string;
}
type ClassObject<ThemeGroupIds extends string> = Record<string, readonly ClassDefinition<ThemeGroupIds>[]>;
/**
 * Hack from https://stackoverflow.com/questions/56687668/a-way-to-disable-type-argument-inference-in-generics/56688073#56688073
 *
 * Could be replaced with NoInfer utility type from TypeScript (https://www.typescriptlang.org/docs/handbook/utility-types.html#noinfertype), but that is only supported in TypeScript 5.4 or higher, so I should wait some time before using it.
 */
type NoInfer<T> = [T][T extends any ? 0 : never];
/**
 * Special-purpose NoInfer variant for string unions used in array item positions.
 *
 * The NoInfer helper above doesn't prevent inference from array items in all cases, so this keeps config arrays like `postfixLookupClassGroups` from defining or narrowing class group IDs. Once tailwind-merge only supports TypeScript 5.4 and newer, this can be replaced with TypeScript's built-in NoInfer utility type.
 */
type NoInferString<T extends string> = T extends infer S ? S & string : never;
type AnyClassGroupIds = string;
type AnyThemeGroupIds = string;
/**
 * type of the tailwind-merge configuration that allows for any possible configuration.
 */
type AnyConfig = Config<AnyClassGroupIds, AnyThemeGroupIds>;

declare const createClassGroupUtils: (config: AnyConfig) => {
    getClassGroupId: (className: string) => string | undefined;
    getConflictingClassGroupIds: (classGroupId: AnyClassGroupIds, hasPostfixModifier: boolean) => readonly AnyClassGroupIds[];
};

/**
 * Observes every successful class-map lookup needed to merge a single candidate. Build-time pruning needs intermediate base matches as well as a final slash match, or removing a base matcher can change runtime precedence.
 * Reuse the merge engine rather than reproducing its decisions. Instrumentation stays in this tooling-only entry point and adds no work to normal merging.
 */
declare const createClassGroupLookup: (config: AnyConfig) => (className: string) => {
    className: string;
    classGroupId: string;
}[];

declare const createParseClassName: (config: AnyConfig) => (className: string) => ParsedClassName;

export { createClassGroupLookup, createClassGroupUtils, createParseClassName };
export type { AnyConfig, ClassGroup, ThemeGetter };
