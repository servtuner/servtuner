import { Fragment } from "react";
import { describeKnobs, useKnobText } from "./knobText";

/** A knob's label, with its flag on hover. */
export function KnobName({ name }: { name: string }) {
  const text = useKnobText();
  return <span title={text.flag(name)}>{text.label(name)}</span>;
}

/** Knob values as "Max sequences 128, Prefix caching on", each label carrying its flag on hover. */
export function KnobValues({ knobs, names }: { knobs: Record<string, unknown>; names?: string[] }) {
  const text = useKnobText();
  const shown = names && names.length > 0 ? names : Object.keys(knobs);
  if (shown.length === 0) return <>{describeKnobs(text, knobs, names)}</>;
  return (
    <>
      {shown.map((name, index) => (
        <Fragment key={name}>
          {index > 0 && ", "}
          <span title={text.flag(name)}>{text.label(name)}</span> {text.value(name, knobs[name])}
        </Fragment>
      ))}
    </>
  );
}
