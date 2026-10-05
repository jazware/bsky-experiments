"use client";
var __defProp = Object.defineProperty;
var __name = (target, value) => __defProp(target, "name", { value, configurable: true });

// src/label.tsx
import * as React from "react";
import { Primitive } from "@radix-ui/react-primitive";
import { jsx } from "react/jsx-runtime";
var Label = /* @__PURE__ */ React.forwardRef(
  /* @__PURE__ */ __name(function Label2(props, forwardedRef) {
    return /* @__PURE__ */ jsx(
      Primitive.label,
      {
        ...props,
        ref: forwardedRef,
        onMouseDown: (event) => {
          const target = event.target;
          if (target.closest("button, input, select, textarea")) return;
          props.onMouseDown?.(event);
          if (!event.defaultPrevented && event.detail > 1) event.preventDefault();
        }
      }
    );
  }, "Label")
);
var Root = Label;
export {
  Label,
  Root
};
//# sourceMappingURL=index.mjs.map
