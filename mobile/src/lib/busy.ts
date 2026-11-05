// Something the person is in the middle of: an open sheet, the "Finished it?"
// check, a change on its way to the server. An update waiting when the app
// comes back goes in only while nothing is (lib/updates), so the restart never
// drops a watch, a rating or a typed word.
let holds = 0;

/** Marks something in progress; call what it returns once it is over. */
export function holdBusy(): () => void {
  holds += 1;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    holds -= 1;
  };
}

export function isBusy(): boolean {
  return holds > 0;
}
