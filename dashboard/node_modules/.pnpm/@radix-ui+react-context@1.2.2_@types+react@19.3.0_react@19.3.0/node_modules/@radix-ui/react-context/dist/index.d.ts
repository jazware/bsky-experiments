import * as React from 'react';

type ContextProvider<ContextValueType extends object | null> = React.FC<ContextValueType & {
    children: React.ReactNode;
}>;
interface UseAssertedContext<ContextValueType extends object | null> {
    (consumerName: string): ContextValueType;
}
interface UseContext<ContextValueType extends object | null> {
    (consumerName: string): ContextValueType;
    (consumerName: string, options: {
        optional: true;
    }): ContextValueType | undefined;
}
declare function createContext<ContextValueType extends object | null>(rootComponentName: string): readonly [ContextProvider<ContextValueType>, UseContext<ContextValueType>];
declare function createContext<ContextValueType extends object | null>(rootComponentName: string, defaultContext: ContextValueType): readonly [ContextProvider<ContextValueType>, UseAssertedContext<ContextValueType>];
type Scope<C = any> = {
    [scopeName: string]: React.Context<C>[];
} | undefined;
type ScopeHook = (scope: Scope) => {
    [__scopeProp: string]: Scope;
};
interface CreateScope {
    scopeName: string;
    (): ScopeHook;
}
type ScopedContextProvider<ContextValueType extends object | null> = React.FC<ContextValueType & {
    scope: Scope<ContextValueType>;
    children: React.ReactNode;
}>;
interface UseScopedContext<ContextValueType extends object | null> {
    (consumerName: string, scope: Scope<ContextValueType | undefined>): ContextValueType;
    (consumerName: string, scope: Scope<ContextValueType | undefined>, options: {
        optional?: true;
    }): ContextValueType | undefined;
}
interface UseAssertedScopedContext<ContextValueType extends object | null> {
    (consumerName: string, scope: Scope<ContextValueType | undefined>): ContextValueType;
}
interface CreateScopedContext {
    <ContextValueType extends object | null>(rootComponentName: string): readonly [ScopedContextProvider<ContextValueType>, UseScopedContext<ContextValueType>];
    <ContextValueType extends object | null>(rootComponentName: string, defaultContext: ContextValueType): readonly [ScopedContextProvider<ContextValueType>, UseAssertedScopedContext<ContextValueType>];
}
declare function createContextScope(scopeName: string, createContextScopeDeps?: CreateScope[]): readonly [CreateScopedContext, CreateScope];

export { type CreateScope, type Scope, createContext, createContextScope };
