"use client";
var __defProp = Object.defineProperty;
var __name = (target, value) => __defProp(target, "name", { value, configurable: true });

// src/alert-dialog.tsx
import * as React from "react";
import { createContextScope } from "@radix-ui/react-context";
import { useComposedRefs } from "@radix-ui/react-compose-refs";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { createDialogScope } from "@radix-ui/react-dialog";
import { composeEventHandlers } from "@radix-ui/primitive";
import { jsx } from "react/jsx-runtime";
var ROOT_NAME = "AlertDialog";
var [createAlertDialogContext, createAlertDialogScope] = createContextScope(ROOT_NAME, [
  createDialogScope
]);
var useDialogScope = createDialogScope();
var AlertDialog = /* @__PURE__ */ __name((props) => {
  const { __scopeAlertDialog, ...alertDialogProps } = props;
  const dialogScope = useDialogScope(__scopeAlertDialog);
  return /* @__PURE__ */ jsx(DialogPrimitive.Root, { ...dialogScope, ...alertDialogProps, modal: true });
}, "AlertDialog");
var AlertDialogTrigger = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogTrigger2(props, forwardedRef) {
    const { __scopeAlertDialog, ...triggerProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    return /* @__PURE__ */ jsx(DialogPrimitive.Trigger, { ...dialogScope, ...triggerProps, ref: forwardedRef });
  }, "AlertDialogTrigger")
);
var AlertDialogPortal = /* @__PURE__ */ __name((props) => {
  const { __scopeAlertDialog, ...portalProps } = props;
  const dialogScope = useDialogScope(__scopeAlertDialog);
  return /* @__PURE__ */ jsx(DialogPrimitive.Portal, { ...dialogScope, ...portalProps });
}, "AlertDialogPortal");
var AlertDialogOverlay = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogOverlay2(props, forwardedRef) {
    const { __scopeAlertDialog, ...overlayProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    return /* @__PURE__ */ jsx(DialogPrimitive.Overlay, { ...dialogScope, ...overlayProps, ref: forwardedRef });
  }, "AlertDialogOverlay")
);
var CONTENT_NAME = "AlertDialogContent";
var [AlertDialogContentProvider, useAlertDialogContentContext] = createAlertDialogContext(CONTENT_NAME);
var AlertDialogContent = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogContent2(props, forwardedRef) {
    const { __scopeAlertDialog, children, ...contentProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    const contentRef = React.useRef(null);
    const composedRefs = useComposedRefs(forwardedRef, contentRef);
    const cancelRef = React.useRef(null);
    return /* @__PURE__ */ jsx(AlertDialogContentProvider, { scope: __scopeAlertDialog, cancelRef, children: /* @__PURE__ */ jsx(
      DialogPrimitive.Content,
      {
        role: "alertdialog",
        ...dialogScope,
        ...contentProps,
        ref: composedRefs,
        onOpenAutoFocus: composeEventHandlers(contentProps.onOpenAutoFocus, (event) => {
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
    return /* @__PURE__ */ jsx(DialogPrimitive.Title, { ...dialogScope, ...titleProps, ref: forwardedRef });
  }, "AlertDialogTitle")
);
var AlertDialogDescription = React.forwardRef(/* @__PURE__ */ __name(function AlertDialogDescription2(props, forwardedRef) {
  const { __scopeAlertDialog, ...descriptionProps } = props;
  const dialogScope = useDialogScope(__scopeAlertDialog);
  return /* @__PURE__ */ jsx(DialogPrimitive.Description, { ...dialogScope, ...descriptionProps, ref: forwardedRef });
}, "AlertDialogDescription"));
var AlertDialogAction = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogAction2(props, forwardedRef) {
    const { __scopeAlertDialog, ...actionProps } = props;
    const dialogScope = useDialogScope(__scopeAlertDialog);
    return /* @__PURE__ */ jsx(DialogPrimitive.Close, { ...dialogScope, ...actionProps, ref: forwardedRef });
  }, "AlertDialogAction")
);
var CANCEL_NAME = "AlertDialogCancel";
var AlertDialogCancel = React.forwardRef(
  /* @__PURE__ */ __name(function AlertDialogCancel2(props, forwardedRef) {
    const { __scopeAlertDialog, ...cancelProps } = props;
    const { cancelRef } = useAlertDialogContentContext(CANCEL_NAME, __scopeAlertDialog);
    const dialogScope = useDialogScope(__scopeAlertDialog);
    const ref = useComposedRefs(forwardedRef, cancelRef);
    return /* @__PURE__ */ jsx(DialogPrimitive.Close, { ...dialogScope, ...cancelProps, ref });
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
export {
  Action,
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogOverlay,
  AlertDialogPortal,
  AlertDialogTitle,
  AlertDialogTrigger,
  Cancel,
  Content2 as Content,
  Description2 as Description,
  Overlay2 as Overlay,
  Portal2 as Portal,
  Root2 as Root,
  Title2 as Title,
  Trigger2 as Trigger,
  createAlertDialogScope
};
//# sourceMappingURL=index.mjs.map
