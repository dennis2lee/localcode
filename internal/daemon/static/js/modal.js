// A modal's open/closed state lives here, in a plain boolean, rather than
// being read back out of the DOM by asking the element whether it still
// carries the styling class. Treating the class list as the state works
// right up until something else touches it — a stylesheet change, an
// animation helper, a second code path that hides the element a different
// way — and then the "is it open?" checks scattered around the app quietly
// start answering wrong. The class is an *output* of this object, never an
// input to it.
export class Modal {
  constructor(el) {
    this.el = el;
    this.isOpen = false;
  }

  open() {
    this.isOpen = true;
    this.el.classList.add('open');
  }

  close() {
    this.isOpen = false;
    this.el.classList.remove('open');
  }
}
