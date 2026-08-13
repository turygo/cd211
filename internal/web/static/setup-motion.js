// Setup wizard step motion.
//
// The wizard state, step gating, and form semantics stay server-owned; this
// module only makes the native POST/redirect navigation read direction and
// animates presentation. Forward/back direction is derived, never guessed:
// the previous page records the step it rendered in sessionStorage, so a
// later step is "forward" and an earlier one is "back" — for fresh loads,
// bfcache restores, and reloads alike. The Navigation API is deliberately
// not a correctness dependency. Reduced motion skips the whole enhancement
// layer in JS, and the CSS media query removes the remaining visual motion,
// leaving state, icons, errors, and focus placement intact.
import { reducedMotion, motionTiming } from "/static/motion.js?v=1";

const stepStorageKey = "cd211-setup-step";
const vtStorageKey = "cd211-setup-vt";

const readStorage = (key) => {
  try {
    return sessionStorage.getItem(key);
  } catch {
    return null;
  }
};

const writeStorage = (key, value) => {
  try {
    sessionStorage.setItem(key, value);
  } catch {
    // Storage unavailable: fall back to plain native navigation.
  }
};

const content = document.querySelector(".setup-content");
const stepper = document.querySelector(".setup-stepper");

if (content && stepper) {
  const currentStep = Number.parseInt(content.dataset.setupStep || "", 10);

  // The outgoing page records whether the browser actually ran a
  // cross-document view transition, so the incoming page only enhances when
  // the mechanism is available. Without it, navigation stays native.
  window.addEventListener("pageswap", (event) => {
    writeStorage(vtStorageKey, event.viewTransition ? "1" : "0");
  });

  // The first meaningful control of each step, placed only after a step
  // transition. An already-focused element (native autofocus or user
  // interaction) is left alone; focus is never animated or stolen.
  const focusTargets = {
    1: "#setup-password",
    2: "#cd2-address",
    3: "#cloud-root",
    4: ".setup-review-form button[type='submit']",
  };
  const placeFocus = (step) => {
    const selector = focusTargets[step];
    if (!selector) {
      return;
    }
    const target = document.querySelector(selector);
    if (!(target instanceof HTMLElement) || !target.isConnected) {
      return;
    }
    if (document.activeElement && document.activeElement !== document.body) {
      return;
    }
    target.focus({ preventScroll: false });
  };

  const stepTransition = () => {
    let direction = null;
    if (Number.isInteger(currentStep)) {
      const previous = Number.parseInt(readStorage(stepStorageKey) || "", 10);
      if (Number.isInteger(previous) && previous !== currentStep) {
        direction = currentStep > previous ? "forward" : "back";
      }
      writeStorage(stepStorageKey, String(currentStep));
    }
    const viewTransitionRan = readStorage(vtStorageKey) === "1";
    writeStorage(vtStorageKey, "0");
    if (direction === null || !viewTransitionRan || reducedMotion.matches) {
      return;
    }

    // Directional slide of the setup content (CSS :view-transition pseudo
    // elements keyed off these classes); the rail itself stays steady.
    document.documentElement.classList.add(`vt-${direction}`);
    // Re-assert synchronously at reveal so the incoming snapshot carries the
    // directional slide even if this module ran before the transition start.
    window.addEventListener("pagereveal", () => {
      document.documentElement.classList.add(`vt-${direction}`);
    });
    window.setTimeout(() => {
      document.documentElement.classList.remove("vt-forward", "vt-back");
    }, motionTiming.emphasized + 160);

    if (direction === "forward") {
      // Completed node and connector advance before the next node activates.
      const steps = Array.from(stepper.querySelectorAll(".setup-step"));
      const justCompleted = steps[currentStep - 2];
      const activating = steps[currentStep - 1];
      window.setTimeout(() => {
        if (justCompleted) {
          justCompleted.classList.add("is-just-completed");
        }
      }, 100);
      window.setTimeout(() => {
        if (activating) {
          activating.classList.add("is-activating");
        }
      }, 100 + motionTiming.standard);
      window.setTimeout(() => {
        placeFocus(currentStep);
      }, 100 + motionTiming.standard + motionTiming.emphasized + 40);
    } else {
      // Back: the rail already shows the reached state; the incoming content
      // slides in from the opposite side, then focus is placed.
      window.setTimeout(() => placeFocus(currentStep), motionTiming.emphasized + 60);
    }
  };

  stepTransition();
  // bfcache restores re-enter a live document: re-derive direction so Back
  // keeps the reverse transition and focus placement.
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) {
      stepTransition();
    }
  });
}
