// Shared motion wrapper for the CD211 operator UI.
//
// Single policy around Motion Mini for every JS-driven transition. All
// durations are milliseconds and follow the motion language in
// docs/ref/linear-design-tokens.md: instant 90, fast 160, standard 220,
// emphasized 300, progress 350-600 by delta, completion hold 1200, precise
// ease-out. Under prefers-reduced-motion the final values apply synchronously
// and the returned promises resolve immediately, so completion-sensitive
// flows (dialog close) keep working unchanged. Pages keep working without JS:
// this module only enhances, never gates, interaction.

import { animate } from "/static/vendor/motion-mini.js?v=13.1.0";

/** The MediaQueryList for `(prefers-reduced-motion: reduce)`. */
export const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

/** Frozen semantic timings shared by every motion consumer. */
export const motionTiming = Object.freeze({
  instant: 90,
  fast: 160,
  standard: 220,
  emphasized: 300,
  progressMin: 350,
  progressMax: 600,
  completionHold: 1200,
  ease: Object.freeze([0.25, 0.1, 0.25, 1]),
});

function finalFrame(keyframes) {
  return Array.isArray(keyframes) ? keyframes[keyframes.length - 1] ?? {} : keyframes;
}

// Applies the final keyframe synchronously, composing Motion Mini's transform
// shortcuts (x/y/scale/rotate) into a single transform so the resting state
// matches what an animated run would leave behind. Property values may be
// scalars or arrays ({opacity: [0, 1]}); the final (last) value wins.
function applyFrame(element, frame) {
  const transforms = [];
  for (const [property, rawValue] of Object.entries(frame)) {
    const value = Array.isArray(rawValue) ? rawValue[rawValue.length - 1] : rawValue;
    switch (property) {
      case "transform":
        transforms.push(value);
        break;
      case "x":
        transforms.push(`translateX(${value}px)`);
        break;
      case "y":
        transforms.push(`translateY(${value}px)`);
        break;
      case "scale":
        transforms.push(`scale(${value})`);
        break;
      case "rotate":
        transforms.push(`rotate(${value}deg)`);
        break;
      default:
        if (property.includes("-")) {
          element.style.setProperty(property, value);
        } else {
          element.style[property] = String(value);
        }
    }
  }
  if (transforms.length > 0) {
    element.style.transform = transforms.join(" ");
  }
}

function clearFrame(element, keyframes) {
  const frame = finalFrame(keyframes);
  for (const property of Object.keys(frame)) {
    if (property === "transform" || property === "x" || property === "y" || property === "scale" || property === "rotate") {
      element.style.transform = "";
    } else if (property.includes("-")) {
      element.style.removeProperty(property);
    } else {
      element.style[property] = "";
    }
  }
}

// The vendored animate() only accepts a keyframes object whose values are
// scalars or property arrays ({opacity: [0, 1]}): it iterates the object's
// keys with for...in, so an array-of-frames input would be read as numeric
// indices. Normalize array-of-frames to property arrays up front, padding
// properties missing from later frames with their previous value so every
// array has one entry per frame.
function normalizeKeyframes(keyframes) {
  if (!Array.isArray(keyframes)) {
    return keyframes;
  }
  const normalized = {};
  for (const frame of keyframes) {
    for (const [property, value] of Object.entries(frame)) {
      (normalized[property] ??= []).push(value);
    }
    for (const property of Object.keys(normalized)) {
      if (!(property in frame)) {
        normalized[property].push(normalized[property][normalized[property].length - 1]);
      }
    }
  }
  return normalized;
}

// The CSS properties a keyframes object animates. Transform shortcuts share
// the element's transform, so they collide with "transform" and with each
// other; every other property is its own group.
function keyframeProperties(keyframes) {
  const properties = new Set();
  for (const property of Object.keys(keyframes)) {
    properties.add(property === "x" || property === "y" || property === "scale" || property === "rotate" ? "transform" : property);
  }
  return properties;
}

// Tracks in-flight wrapper animations per (element, property group). Motion
// Mini's replace path stops the previous animation without settling its
// finished promise (vendor stop() commits styles but never notifiesFinished),
// and it replaces per (element, property), so the wrapper owns interruption:
// a new call stops the still-running states whose keyframes overlap this
// call's properties (their Motion controls .stop() commit current styles,
// then the wrapper settles and runs clearInline cleanup) before the new run
// starts. Non-overlapping animations (e.g. an entrance's opacity/transform
// followed by a boxShadow on the same row) run concurrently, mirroring the
// vendor's grouping.
const pendingElementAnimations = new WeakMap();

/**
 * Drives an element to a final visual state with Motion Mini. Options use
 * milliseconds (duration, delay); `ease` passes through to Motion Mini and
 * `clearInline` removes the inline styles written for the animation once the
 * resting state is reached. Keyframes may be an object of scalar or
 * property-array values, or an array of frame objects (normalized internally
 * before dispatch). Under reduced motion (or a non-positive duration) the
 * final keyframe applies synchronously and the returned promise resolves
 * immediately. Starting an animation on the same element with overlapping
 * properties cancels the previous one: the interrupted promise settles
 * (resolving after its clearInline cleanup) and the new run takes the element
 * to its own final state, so an interrupted run always ends at a valid final
 * state.
 */
export function animateElement(element, keyframes, options = {}) {
  const duration = options.duration ?? motionTiming.standard;
  const delay = options.delay ?? 0;
  const ease = options.ease ?? motionTiming.ease;
  const clearInline = options.clearInline ?? false;
  const normalized = normalizeKeyframes(keyframes);
  const properties = keyframeProperties(normalized);

  const owned = pendingElementAnimations.get(element);
  if (owned) {
    for (const property of properties) {
      const previous = owned.get(property);
      if (previous) {
        previous.interrupt();
      }
    }
  }

  if (reducedMotion.matches || !(duration > 0)) {
    applyFrame(element, finalFrame(normalized));
    if (clearInline) {
      clearFrame(element, normalized);
    }
    return Promise.resolve();
  }

  let settled = false;
  let resolveSettled;
  const settledPromise = new Promise((resolve) => {
    resolveSettled = resolve;
  });
  let animation = null;
  const state = { interrupt: null, properties };
  const settle = () => {
    if (settled) {
      return;
    }
    settled = true;
    const owned = pendingElementAnimations.get(element);
    if (owned) {
      for (const property of properties) {
        if (owned.get(property) === state) {
          owned.delete(property);
        }
      }
      if (owned.size === 0) {
        pendingElementAnimations.delete(element);
      }
    }
    if (clearInline) {
      clearFrame(element, normalized);
    }
    resolveSettled();
  };
  state.interrupt = () => {
    if (animation) {
      animation.stop();
    }
    settle();
  };

  try {
    animation = animate(element, normalized, {
      duration: duration / 1000,
      delay: delay / 1000,
      ease,
    });
  } catch (error) {
    settle();
    return Promise.reject(error);
  }

  let owners = pendingElementAnimations.get(element);
  if (!owners) {
    owners = new Map();
    pendingElementAnimations.set(element, owners);
  }
  for (const property of properties) {
    owners.set(property, state);
  }
  Promise.resolve(animation).then(settle, settle);

  return settledPromise;
}

/**
 * Interpolates a numeric value monotonically from `from` to `to` over
 * `duration` milliseconds (default progressMin), calling `onUpdate` with each
 * frame. Reduced motion and non-positive durations apply `to` immediately.
 * Every call is independent (concurrent rows may interpolate at once); the
 * caller owns supersession by cancelling the previous handle when a new run
 * replaces it. The returned handle exposes `cancel()` and a `finished`
 * promise.
 */
export function animateNumber({ from, to, duration = motionTiming.progressMin, onUpdate }) {
  if (from === to) {
    onUpdate(to);
    return { cancel() {}, finished: Promise.resolve() };
  }
  if (reducedMotion.matches || !(duration > 0)) {
    onUpdate(to);
    return { cancel() {}, finished: Promise.resolve() };
  }

  const state = { cancelled: false };
  const started = performance.now();
  let resolveFinished;
  const finished = new Promise((resolve) => {
    resolveFinished = resolve;
  });

  const tick = (now) => {
    if (state.cancelled) {
      return;
    }
    const progress = Math.min(1, (now - started) / duration);
    const eased = 1 - Math.pow(1 - progress, 3);
    onUpdate(from + (to - from) * eased);
    if (progress < 1) {
      state.raf = requestAnimationFrame(tick);
    } else {
      resolveFinished();
    }
  };

  state.raf = requestAnimationFrame(tick);

  return {
    cancel() {
      if (state.cancelled) {
        return;
      }
      state.cancelled = true;
      cancelAnimationFrame(state.raf);
      resolveFinished();
    },
    finished,
  };
}

/**
 * Stagger delay for revealed lists, capped so large sets never delay their
 * final rows.
 */
export function staggerDelay(index, step, cap) {
  return Math.min(index * step, cap);
}
