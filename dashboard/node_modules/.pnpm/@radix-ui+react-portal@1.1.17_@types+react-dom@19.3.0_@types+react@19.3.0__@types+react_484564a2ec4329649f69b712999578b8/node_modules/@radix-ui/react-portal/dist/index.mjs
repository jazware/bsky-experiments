"use client";
var __defProp = Object.defineProperty;
var __name = (target, value) => __defProp(target, "name", { value, configurable: true });

// src/portal.tsx
import * as React from "react";
import * as ReactDOM from "react-dom";
import { Primitive } from "@radix-ui/react-primitive";
import { useLayoutEffect } from "@radix-ui/react-use-layout-effect";
import { jsx } from "react/jsx-runtime";
var Portal = /* @__PURE__ */ React.forwardRef(
  /* @__PURE__ */ __name(function Portal2(props, forwardedRef) {
    const { container: containerProp, ...portalProps } = props;
    const [mounted, setMounted] = React.useState(false);
    useLayoutEffect(() => setMounted(true), []);
    const container = containerProp || mounted && globalThis?.document?.body;
    return container ? ReactDOM.createPortal(/* @__PURE__ */ jsx(Primitive.div, { ...portalProps, ref: forwardedRef }), container) : null;
  }, "Portal")
);
var Root = Portal;
export {
  Portal,
  Root
};
//# sourceMappingURL=index.mjs.map
