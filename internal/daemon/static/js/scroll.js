// Following the bottom of a live transcript, and letting go of it the
// moment the reader scrolls away.
//
// Every append used to end with `el.scrollTop = el.scrollHeight`, which
// means a model writing a long answer drags the view back to the bottom
// several times a second. Reading anything further up while a turn runs
// was impossible: the page yanked itself away mid-sentence. Reported as
// "scrolling up moves the view back to where the model is writing".
//
// The rule is the one the TUI has had since v0.31.0: follow the bottom
// only when the view was already at the bottom. Scroll up and the view
// stays where it was put, however much arrives underneath it; scroll back
// down and following resumes on its own, because being at the bottom is
// the whole condition. Nothing to turn on, nothing to remember.
//
// "Was already at the bottom" is measured immediately before the content
// changes rather than kept as a flag updated from scroll events. Two
// reasons, and the first is the important one: after an append the
// element is by definition no longer at the bottom, so the measurement
// has to happen first either way — and taking it there means the
// behaviour never depends on a scroll event arriving, which a browser
// delivers asynchronously and throttles in a background tab. A missed
// event can leave a remembered flag wrong for good; a measurement cannot
// be stale, because it is taken at the moment it is used.
const DEFAULT_THRESHOLD = 32;

// createFollower wires one scrolling element.
//
// onChange is called when the view moves to or from the bottom, for a
// caller that shows a "jump to the bottom" control — this module knows
// nothing about that button beyond telling it when to exist. It is the
// one thing here that does listen for scroll events, because it is the
// one thing that can afford to be a frame late.
export function createFollower(el, onChange = () => {}, threshold = DEFAULT_THRESHOLD) {
  // A few pixels of slack: scrollTop is fractional under a zoom or a
  // high-DPI scale factor, so an exact comparison against
  // scrollHeight - clientHeight is false at the bottom of the page often
  // enough to matter.
  const atBottom = () => el.scrollHeight - el.scrollTop - el.clientHeight <= threshold;
  const toBottom = () => { el.scrollTop = el.scrollHeight; };

  let announced = true;
  const notify = () => {
    const now = atBottom();
    if (now === announced) return;
    announced = now;
    onChange(now);
  };

  el.addEventListener('scroll', notify);

  return {
    // keeping runs a change to the content and holds the reader's place
    // across it. Returns whatever the change returned, so a call site
    // stays one expression.
    keeping(mutate) {
      const follow = atBottom();
      const out = mutate();
      if (follow) toBottom();
      notify();
      return out;
    },
    // force is for the things the reader just did — sending a prompt,
    // opening a session, clicking the jump control. Those are a request
    // to be at the bottom, not new output arriving at it.
    force() {
      toBottom();
      notify();
    },
    atBottom,
  };
}
