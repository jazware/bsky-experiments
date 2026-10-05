Object.defineProperty(exports, Symbol.toStringTag, { value: "Module" });
const require_classPrivateFieldSet2 = require("./classPrivateFieldSet2-CvCEk6bM.cjs");
const require_timeoutManager = require("./timeoutManager.cjs");
const require_utils = require("./utils.cjs");
const require_environmentManager = require("./environmentManager.cjs");
//#region src/removable.ts
var _gcTimeout = /* @__PURE__ */ new WeakMap();
/**
* The base class for cache entries that are garbage collected once nothing is using them —
* `Query` and `Mutation` both extend it. `gcTime` controls how long an unused entry is kept.
*/
var Removable = class {
	constructor() {
		require_classPrivateFieldSet2._classPrivateFieldInitSpec(this, _gcTimeout, void 0);
	}
	/**
	* Clears the pending garbage collection timeout, so the entry is no longer scheduled for removal.
	* A subclass may override this to release what it holds on to as well — `Query` also cancels any
	* in-flight fetch.
	*/
	destroy() {
		this.clearGcTimeout();
	}
	scheduleGc() {
		this.clearGcTimeout();
		if (require_utils.isValidTimeout(this.gcTime)) require_classPrivateFieldSet2._classPrivateFieldSet2(_gcTimeout, this, require_timeoutManager.timeoutManager.setTimeout(() => {
			this.optionalRemove();
		}, this.gcTime));
	}
	updateGcTime(newGcTime) {
		this.gcTime = Math.max(this.gcTime || 0, newGcTime ?? (require_environmentManager.isServer() ? Infinity : 3e5));
	}
	clearGcTimeout() {
		if (require_classPrivateFieldSet2._classPrivateFieldGet2(_gcTimeout, this) !== void 0) {
			require_timeoutManager.timeoutManager.clearTimeout(require_classPrivateFieldSet2._classPrivateFieldGet2(_gcTimeout, this));
			require_classPrivateFieldSet2._classPrivateFieldSet2(_gcTimeout, this, void 0);
		}
	}
};
//#endregion
exports.Removable = Removable;

//# sourceMappingURL=removable.cjs.map