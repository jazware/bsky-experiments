//#region ../../crates/rolldown/src/runtime/runtime-base.js
var __create = Object.create;
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __getProtoOf = Object.getPrototypeOf;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __exportAll = (all, no_symbols) => {
	let target = {};
	for (var name in all) __defProp(target, name, {
		get: all[name],
		enumerable: true
	});
	if (!no_symbols) __defProp(target, Symbol.toStringTag, { value: "Module" });
	return target;
};
var __copyProps = (to, from, except, desc) => {
	if (from && typeof from === "object" || typeof from === "function") for (var keys = __getOwnPropNames(from), i = 0, n = keys.length, key; i < n; i++) {
		key = keys[i];
		if (!__hasOwnProp.call(to, key) && key !== except) __defProp(to, key, {
			get: ((k) => from[k]).bind(null, key),
			enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable
		});
	}
	return to;
};
var __reExport = (target, mod, secondTarget) => (__copyProps(target, mod, "default"), secondTarget && __copyProps(secondTarget, mod, "default"));
var __toESM = (mod, isNodeMode, target) => (target = mod != null ? __create(__getProtoOf(mod)) : {}, __copyProps(isNodeMode || !mod || !mod.__esModule || !__hasOwnProp.call(mod, "default") ? __defProp(target, "default", {
	value: mod,
	enumerable: true
}) : target, mod));
var __toCommonJS = (mod) => __hasOwnProp.call(mod, "module.exports") ? mod["module.exports"] : __copyProps(__defProp({}, "__esModule", { value: true }), mod);
//#endregion
//#region ../../crates/rolldown_plugin_hmr/src/runtime/runtime-extra-dev-common.js
var Module = class {
	/**
	* @type {{ exports: any }}
	*/
	exportsHolder = { exports: null };
	/**
	* @type {string}
	*/
	id;
	/**
	* @param {string} id
	*/
	constructor(id) {
		this.id = id;
	}
	get exports() {
		return this.exportsHolder.exports;
	}
};
/**
* Compiler-emitted module-graph delta — topology (static + dynamic edges).
* `ids[0, localCount)` are the modules this payload carries; `ids[localCount, …)` are foreign edge targets.
* `edges[i]` / `dynamicEdges[i]` are the static / dynamic-`import()` out-edges of `ids[i]`.
* `bindings[i][j]` are the export names `ids[i]` imports through `edges[i][j]`. `bindings` holds
* only some rows; a missing row, or a missing or `null` entry, means the whole namespace.
* `dynamicEdges` also holds only some rows; a missing row means no dynamic edges.
* @typedef {{ ids: string[], localCount: number, edges: number[][], bindings?: Record<number, (string[] | null)[]>, dynamicEdges?: Record<number, number[]> }} ModuleGraphDelta
* @typedef {{ createModuleHotContext(moduleId: string): any, onModuleCacheRemoval(moduleId: string): void }} DevRuntimeHooks
*/
var MissingFactoryError = class extends Error {
	/**
	* @param {string} id
	*/
	constructor(id) {
		super(`No factory registered for module ${id}`);
		this.id = id;
	}
};
var DevRuntime = class {
	/**
	* Client ID generated at runtime initialization, used for lazy compilation requests.
	* @type {string}
	*/
	clientId;
	/**
	* @param {string} clientId
	*/
	constructor(clientId) {
		this.clientId = clientId;
	}
	/**
	* Static import edges from `registerGraph` — entries persist across `removeModuleCache`
	* and change only by replacement from a newer payload (last write wins).
	* `bindings` is the payload row as sent; `getImportedBindings` resolves a missing entry.
	* @type {Map<string, { edges: string[], bindings: (string[] | null)[] | undefined }>}
	*/
	staticImports = /* @__PURE__ */ new Map();
	/**
	* Reverse index over the static imports.
	* @type {Map<string, Set<string>>}
	*/
	importers = /* @__PURE__ */ new Map();
	/**
	* Dynamic `import()` edges from `registerGraph`, keyed by importer — mirror of
	* `staticImports` for the dynamic reverse index.
	* @type {Map<string, { edges: string[] }>}
	*/
	dynamicImports = /* @__PURE__ */ new Map();
	/**
	* Reverse index over the dynamic imports.
	* @type {Map<string, Set<string>>}
	*/
	dynamicImporters = /* @__PURE__ */ new Map();
	/**
	* The module cache. Membership means "this module's side effects ran in this tab" —
	* registration is emitted ahead of every module body, and nothing un-registers on
	* unwind, so a factory that throws mid-body stays registered. A `Map` rather than a
	* plain object: HMR eviction deletes entries, and a `delete` on an object drops V8
	* into dictionary mode, taxing every later lookup on the hottest read path.
	* @type {Map<string, Module>}
	*/
	moduleCache = /* @__PURE__ */ new Map();
	/**
	* Re-runnable factories from HMR patches and lazy chunks. The initial bundle stays
	* scope-hoisted and contributes none.
	* @type {Map<string, (id: string) => void>}
	*/
	factories = /* @__PURE__ */ new Map();
	/**
	* Installed by the dev client at boot. The runtime is a store + executor and makes
	* no HMR decisions; accepting, disposing, and reloading live behind these hooks.
	* @type {DevRuntimeHooks | null}
	*/
	hooks = null;
	/**
	* @param {ModuleGraphDelta} delta
	*/
	registerGraph(delta) {
		for (let i = 0; i < delta.localCount; i++) {
			const id = delta.ids[i];
			const edges = delta.edges[i].map((j) => delta.ids[j]);
			for (const target of this.staticImports.get(id)?.edges ?? []) this.importers.get(target)?.delete(id);
			for (const target of edges) {
				let importerSet = this.importers.get(target);
				if (!importerSet) {
					importerSet = /* @__PURE__ */ new Set();
					this.importers.set(target, importerSet);
				}
				importerSet.add(id);
			}
			this.staticImports.set(id, {
				edges,
				bindings: delta.bindings?.[i]
			});
			const dynamicEdges = (delta.dynamicEdges?.[i] ?? []).map((j) => delta.ids[j]);
			for (const target of this.dynamicImports.get(id)?.edges ?? []) this.dynamicImporters.get(target)?.delete(id);
			for (const target of dynamicEdges) {
				let importerSet = this.dynamicImporters.get(target);
				if (!importerSet) {
					importerSet = /* @__PURE__ */ new Set();
					this.dynamicImporters.set(target, importerSet);
				}
				importerSet.add(id);
			}
			this.dynamicImports.set(id, { edges: dynamicEdges });
		}
	}
	/**
	* @param {string} id
	* @param {(id: string) => void} fn
	*/
	registerFactory(id, fn) {
		this.factories.set(id, fn);
	}
	/**
	* @param {string} id
	* @param {{ exports: any }} [exportsHolder]
	*/
	registerModule(id, exportsHolder = { exports: {} }) {
		const module = new Module(id);
		module.exportsHolder = exportsHolder;
		this.moduleCache.set(id, module);
	}
	/**
	* @param {string} id
	* @returns {string[]}
	*/
	getImporters(id) {
		const dynamic = this.dynamicImporters.get(id);
		if (!dynamic || dynamic.size === 0) return [...this.importers.get(id) ?? []];
		return [.../* @__PURE__ */ new Set([...this.importers.get(id) ?? [], ...dynamic])];
	}
	/**
	* `"*"` means the whole namespace, an empty list a side-effect-only import, `undefined` no edge.
	* @param {string} importer
	* @param {string} id
	* @returns {string[] | undefined}
	*/
	getImportedBindings(importer, id) {
		const record = this.staticImports.get(importer);
		const j = record ? record.edges.indexOf(id) : -1;
		const names = j === -1 ? void 0 : record?.bindings?.[j] ?? ["*"];
		if (!this.dynamicImports.get(importer)?.edges.includes(id)) return names;
		if (!names) return ["*"];
		return names.includes("*") ? names : [...names, "*"];
	}
	/**
	* @param {string} id
	*/
	isExecuted(id) {
		return this.moduleCache.has(id);
	}
	/**
	* @param {string} id
	*/
	hasFactory(id) {
		return this.factories.has(id);
	}
	/**
	* Module-cache delete only — static imports and factories persist. Removal is what
	* re-arms a cache-gated factory for `initModule`.
	* @param {string} id
	*/
	removeModuleCache(id) {
		this.moduleCache.delete(id);
		this.hooks?.onModuleCacheRemoval(id);
	}
	/**
	* The one re-execution gate: registered → return the live exports; otherwise run the
	* mapped factory (which registers itself first, then runs the body).
	* @param {string} id
	*/
	initModule(id) {
		if (this.moduleCache.has(id)) return this.loadExports(id);
		const factory = this.factories.get(id);
		if (!factory) throw new MissingFactoryError(id);
		factory(id);
		return this.loadExports(id);
	}
	/**
	* @param {string} id
	*/
	loadExports(id) {
		const module = this.moduleCache.get(id);
		if (module) return module.exportsHolder.exports;
		else {
			console.warn(`Module ${id} not found`);
			return {};
		}
	}
	/**
	* @param {string} moduleId
	*/
	createModuleHotContext(moduleId) {
		if (this.hooks) return this.hooks.createModuleHotContext(moduleId);
		throw new Error("createModuleHotContext requires installed hooks or an override");
	}
	/** @internal */
	__toESM = __toESM;
	/** @internal */
	__toCommonJS = __toCommonJS;
	/** @internal */
	__exportAll = __exportAll;
	/**
	* @param {boolean} [isNodeMode]
	* @returns {(mod: any) => any}
	* @internal
	*/
	__toDynamicImportESM = (isNodeMode) => (mod) => __toESM(mod.default, isNodeMode);
	/** @internal */
	__reExport = __reExport;
};
//#endregion
export { DevRuntime, MissingFactoryError };
