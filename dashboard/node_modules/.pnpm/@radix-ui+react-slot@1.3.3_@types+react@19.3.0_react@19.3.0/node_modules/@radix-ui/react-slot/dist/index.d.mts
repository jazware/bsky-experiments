import * as React from 'react';

type SlotProps<Elem extends Element = HTMLElement, Props = React.HTMLAttributes<Elem>> = Props & {
    children?: React.ReactNode;
};
declare function createSlot<Elem extends Element = HTMLElement, Props = React.HTMLAttributes<Elem>>(ownerName: string): React.ForwardRefExoticComponent<React.PropsWithoutRef<SlotProps<Elem, Props>> & React.RefAttributes<Elem>>;
declare const Slot: React.ForwardRefExoticComponent<React.HTMLAttributes<HTMLElement> & {
    children?: React.ReactNode;
} & React.RefAttributes<HTMLElement>>;
type SlottableChildrenProps = {
    children: React.ReactNode;
};
type SlottableRenderFnProps = {
    child: React.ReactNode;
    children: (slottable: React.ReactNode) => React.ReactNode;
};
type SlottableProps = SlottableRenderFnProps | SlottableChildrenProps;
interface SlottableComponent extends React.FC<SlottableProps> {
    __radixId: symbol;
}
declare function createSlottable(ownerName: string): SlottableComponent;
declare const Slottable: SlottableComponent;

export { Slot as Root, Slot, type SlotProps, Slottable, createSlot, createSlottable };
