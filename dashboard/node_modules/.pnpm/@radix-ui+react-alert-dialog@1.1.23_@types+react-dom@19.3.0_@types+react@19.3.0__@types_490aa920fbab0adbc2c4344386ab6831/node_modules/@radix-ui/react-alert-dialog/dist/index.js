"use strict";
"use client";
var __create = Object.create;
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __getProtoOf = Object.getPrototypeOf;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __name = (target, value) => __defProp(target, "name", { value, configurable: true });
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: true });
};
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toESM = (mod, isNodeMode, target) => (target = mod != null ? __create(__getProtoOf(mod)) : {}, __copyProps(
  // If the importer is in node compatibility mode or this is not an ESM
  // file that has been converted to a CommonJS file using a Babel-
  // compatible transform (i.e. "__esModule" has not been set), then set
  // "default" to the CommonJS "module.exports" for node compatibility.
  isNodeMode || !mod || !mod.__esModule ? __defProp(target, "default", { value: mod, enumerable: true }) : target,
  mod
));
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

// src/index.ts
var index_exports = {};
__export(index_exports, {
  Action: () => Action,
  AlertDialog: () => AlertDialog,
  AlertDialogAction: () => AlertDialogAction,
  AlertDialogCancel: () => AlertDialogCancel,
  AlertDialogContent: () => AlertDialogContent,
  AlertDialogDescription: () => AlertDialogDescription,
  AlertDialogOverlay: () => AlertDialogOverlay,
  AlertDialogPortal: () => AlertDialogPortal,
  AlertDialogTitle: () => AlertDialogTitle,
  AlertDialogTrigger: () => AlertDialogTrigger,
  Cancel: () => Cancel,
  Content: () => Content2,
  Description: () => Description2,
  Overlay: () => Overlay2,
  Portal: () => Portal2,
  Root: () => Root2,
  Title: () => Title2,
  Trigger: () => Trigger2,
  createAlertDialogScope: () => createAlertDialogScope
});
module.exports = __toCommonJS(index_exports);

// src/alert-dialog.tsx
var React = __toESM(require("react"));
var import_react_context = require("@radix-ui/react-context");
var import_react_compose_refs = require("@radix-ui/react-compose-refs");
var DialogPrimitive = __toESM(require("@radix-ui/react-dialog"));
var import_react_dialog = require("@radix-ui/react-dialog");
var import_primitive = require("@radix-ui/primitive");
var import_jsx_runtime = require("react/jsx-runtime");
var ROOT_NAME = "AlertDialog";
var [createAlertDialogContext, createAlertDialogScope] = (0, import_react_context.createContextScope)(ROOT_NAME, [
  import_react_dialog.createDialogScope
]);
var useDialogScope = (0, import_react_dialog.createDialogScope)();
var AlertDialog = /* @__PURE__ */ __name((props) => {
  const { __scopeAlertDialog, ...alertDialogProps } = props;
  const dialogScope = useDialogScope(__scopeAlertDialog);
  return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(DialogPrimitive.Root, { ...dialogScope, ...alertDialogProps, modal: true });
}, "AlertDialog");
var AlertDialogTrigger = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogTrigger2(props, forwardedRef) {
    const { __scopeAlertDialog, ...triggerProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(DialogPrimitive.Trigger, { ...dialogScope, ...triggerProps, ref: forwardedRef });
  }, "AlertDialogTrigger")
);
var AlertDialogPortal = /* @__PURE__ */ __name((props) => {
  const { __scopeAlertDialog, ...portalProps } = props;
  const dialogScope = useDialogScope(__scopeAlertDialog);
  return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(DialogPrimitive.Portal, { ...dialogScope, ...portalProps });
}, "AlertDialogPortal");
var AlertDialogOverlay = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogOverlay2(props, forwardedRef) {
    const { __scopeAlertDialog, ...overlayProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(DialogPrimitive.Overlay, { ...dialogScope, ...overlayProps, ref: forwardedRef });
  }, "AlertDialogOverlay")
);
var CONTENT_NAME = "AlertDialogContent";
var [AlertDialogContentProvider, useAlertDialogContentContext] = createAlertDialogContext(CONTENT_NAME);
var AlertDialogContent = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogContent2(props, forwardedRef) {
    const { __scopeAlertDialog, children, ...contentProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    const contentRef = React.useRef(null);
    const composedRefs = (0, import_react_compose_refs.useComposedRefs)(forwardedRef, contentRef);
    const cancelRef = React.useRef(null);
    return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(AlertDialogContentProvider, { scope: __scopeAlertDialog, cancelRef, children: /* @__PURE__ */ (0, import_jsx_runtime.jsx)(
      DialogPrimitive.Content,
      {
        role: "alertdialog",
        ...dialogScope,
        ...contentProps,
        ref: composedRefs,
        onOpenAutoFocus: (0, import_primitive.composeEventHandlers)(contentProps.onOpenAutoFocus, (event) => {
          event.preventDefault();
          cancelRef.current?.focus({ preventScroll: true });
        }),
        onPointerDownOutside: (event) => event.preventDefault(),
        onInteractOutside: (event) => event.preventDefault(),
        children
      }
    ) });
  }, "AlertDialogContent")
);
var AlertDialogTitle = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogTitle2(props, forwardedRef) {
    const { __scopeAlertDialog, ...titleProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(DialogPrimitive.Title, { ...dialogScope, ...titleProps, ref: forwardedRef });
  }, "AlertDialogTitle")
);
var AlertDialogDescription = React.forwardRef(/* @__PURE__ */ __name(function AlertDialogDescription2(props, forwardedRef) {
  const { __scopeAlertDialog, ...descriptionProps } = props;
  const dialogScope = useDialogScope(__scopeAlertDialog);
  return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(DialogPrimitive.Description, { ...dialogScope, ...descriptionProps, ref: forwardedRef });
}, "AlertDialogDescription"));
var AlertDialogAction = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogAction2(props, forwardedRef) {
    const { __scopeAlertDialog, ...actionProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(DialogPrimitive.Close, { ...dialogScope, ...actionProps, ref: forwardedRef });
  }, "AlertDialogAction")
);
var CANCEL_NAME = "AlertDialogCancel";
var AlertDialogCancel = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogCancel2(props, forwardedRef) {
    const { __scopeAlertDialog, ...cancelProps } = props;
    const { cancelRef } = useAlertDialogContentContext(CANCEL_NAME, __scopeAlertDialog);
    const dialogScope = useDialogScope(__scopeAlertDialog);
    const ref = (0, import_react_compose_refs.useComposedRefs)(forwardedRef, cancelRef);
    return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(DialogPrimitive.Close, { ...dialogScope, ...cancelProps, ref });
  }, "AlertDialogCancel")
);
var Root2 = AlertDialog;
var Trigger2 = AlertDialogTrigger;
var Portal2 = AlertDialogPortal;
var Overlay2 = AlertDialogOverlay;
var Content2 = AlertDialogContent;
var Action = AlertDialogAction;
var Cancel = AlertDialogCancel;
var Title2 = AlertDialogTitle;
var Description2 = AlertDialogDescription;
//# sourceMappingURL=index.js.map
