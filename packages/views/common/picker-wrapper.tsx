/** Wraps an inline picker trigger so its pointer/click events don't bubble to
 *  the surrounding Link, drag handler, or row/card open handler. Shared by the
 *  board card and list row, which both render date/priority/assignee pickers
 *  inside an interactive container. */
export function PickerWrapper({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  const stop = (e: React.SyntheticEvent) => {
    e.stopPropagation();
    e.preventDefault();
  };
  return (
    <div onClick={stop} onMouseDown={stop} onPointerDown={stop} className={className}>
      {children}
    </div>
  );
}
