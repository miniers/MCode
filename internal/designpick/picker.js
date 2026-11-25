(function () {
  'use strict';

  if (typeof window === 'undefined' || window.__mcodePick) {
    return;
  }

  var STYLE_ID = 'mc-pick-style';
  var OPTION_CLASS = 'mc-pick-option';
  var SELECTED_CLASS = 'mc-pick-selected';
  var DIMMED_CLASS = 'mc-pick-dimmed';
  var BTN_CLASS = 'mc-pick-btn';
  var FLASH_CLASS = 'mc-pick-flash';
  var REDUCE_CLASS = 'mc-pick-reduce-motion';
  var LOCKED_CLASS = 'mc-pick-locked';
  var MSG_SOURCE = 'mcode-pick';
  var FLASH_DURATION_MS = 600;
  var CHECK_SVG =
    '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M20 6 9 17l-5-5"/>' +
    '</svg>';

  var groups = [];
  var version = 0;
  var locked = false;
  var reduceMotionOverride = false;
  var initialized = false;

  function isInIframe() {
    return typeof window.parent !== 'undefined' && window.parent !== null && window.parent !== window;
  }

  function isReduceMotion() {
    if (reduceMotionOverride) return true;
    if (typeof window.matchMedia === 'function') {
      try {
        return !!window.matchMedia('(prefers-reduced-motion: reduce)').matches;
      } catch (_) {
        return false;
      }
    }
    return false;
  }

  function nearestChoiceAncestor(el) {
    var cur = el && el.parentElement;
    if (!cur) return null;
    if (typeof cur.closest === 'function') {
      return cur.closest('[data-mc-choice]');
    }
    while (cur) {
      if (typeof cur.hasAttribute === 'function' && cur.hasAttribute('data-mc-choice')) {
        return cur;
      }
      cur = cur.parentElement;
    }
    return null;
  }

  function collectGroups() {
    groups = [];
    if (typeof document === 'undefined' || typeof document.querySelectorAll !== 'function') {
      return;
    }
    var choiceNodes = document.querySelectorAll('[data-mc-choice]');
    for (var i = 0; i < choiceNodes.length; i++) {
      var groupEl = choiceNodes[i];
      var question = groupEl.getAttribute('data-mc-choice') || '';
      var optionNodes = groupEl.querySelectorAll('[data-mc-option]');
      var options = [];
      var recommendedOption = null;

      for (var j = 0; j < optionNodes.length; j++) {
        var optEl = optionNodes[j];
        if (nearestChoiceAncestor(optEl) !== groupEl) {
          continue;
        }
        var optName = optEl.getAttribute('data-mc-option');
        if (optName === null || optName === '') {
          continue;
        }
        var isRec = optEl.hasAttribute('data-mc-recommended');
        if (isRec && recommendedOption === null) {
          recommendedOption = optName;
        }
        options.push({
          option: optName,
          isRecommended: isRec,
          el: optEl,
          btn: null
        });
      }

      if (options.length < 2) {
        continue;
      }

      var defaultOption = recommendedOption !== null ? recommendedOption : options[0].option;
      groups.push({
        question: question,
        el: groupEl,
        options: options,
        defaultOption: defaultOption,
        selected: defaultOption,
        flashTimer: null
      });
    }
  }

  function injectStyle() {
    if (typeof document === 'undefined' || !document.createElement) return;
    if (typeof document.getElementById === 'function' && document.getElementById(STYLE_ID)) {
      return;
    }
    var style = document.createElement('style');
    style.id = STYLE_ID;
    style.textContent = [
      '.' + OPTION_CLASS + ' {',
      '  transition: opacity var(--mc-dur-snap, 140ms) cubic-bezier(0.22, 1, 0.36, 1), outline-color var(--mc-dur-fast, 90ms) cubic-bezier(0.2, 0.9, 0.3, 1);',
      '}',
      '.' + OPTION_CLASS + '.' + SELECTED_CLASS + ' {',
      '  opacity: 1;',
      '  outline: 2px solid var(--mc-accent, #0062CC);',
      '}',
      '.' + OPTION_CLASS + '.' + DIMMED_CLASS + ' {',
      '  opacity: 0.45;',
      '}',
      '.' + OPTION_CLASS + '.' + DIMMED_CLASS + ':hover,',
      '.' + OPTION_CLASS + '.' + DIMMED_CLASS + ':focus-within {',
      '  opacity: 1;',
      '}',
      '.' + BTN_CLASS + ' {',
      '  position: absolute;',
      '  top: 8px;',
      '  right: 8px;',
      '  z-index: 2;',
      '  box-sizing: border-box;',
      '  width: 24px;',
      '  height: 24px;',
      '  padding: 0;',
      '  margin: 0;',
      '  border-radius: 999px;',
      '  border: 1.5px solid var(--mc-border-strong, rgba(0, 0, 0, 0.22));',
      '  background: var(--mc-surface-card, #FFFFFF);',
      '  color: var(--mc-on-accent, #FFFFFF);',
      '  display: inline-flex;',
      '  align-items: center;',
      '  justify-content: center;',
      '  cursor: pointer;',
      '  opacity: 0;',
      '  transform: scale(0.85);',
      '  transition: opacity var(--mc-dur-fast, 90ms) cubic-bezier(0.2, 0.9, 0.3, 1), transform var(--mc-dur-snap, 140ms) cubic-bezier(0.2, 0.9, 0.3, 1), background-color var(--mc-dur-fast, 90ms) cubic-bezier(0.2, 0.9, 0.3, 1), border-color var(--mc-dur-fast, 90ms) cubic-bezier(0.2, 0.9, 0.3, 1);',
      '}',
      '.' + BTN_CLASS + ':focus-visible {',
      '  outline: 2px solid var(--mc-accent, #0062CC);',
      '  outline-offset: 2px;',
      '}',
      '.' + OPTION_CLASS + ':hover > .' + BTN_CLASS + ',',
      '.' + OPTION_CLASS + ':focus-within > .' + BTN_CLASS + ',',
      '.' + OPTION_CLASS + '.' + SELECTED_CLASS + ' > .' + BTN_CLASS + ' {',
      '  opacity: 1;',
      '  transform: scale(1);',
      '}',
      '.' + OPTION_CLASS + '.' + SELECTED_CLASS + ' > .' + BTN_CLASS + ' {',
      '  background: var(--mc-accent, #0062CC);',
      '  border-color: var(--mc-accent, #0062CC);',
      '}',
      '.' + LOCKED_CLASS + ' .' + OPTION_CLASS + '.' + DIMMED_CLASS + ':hover,',
      '.' + LOCKED_CLASS + ' .' + OPTION_CLASS + '.' + DIMMED_CLASS + ':focus-within {',
      '  opacity: 0.45;',
      '}',
      '.' + LOCKED_CLASS + ' .' + OPTION_CLASS + ':not(.' + SELECTED_CLASS + ') > .' + BTN_CLASS + ',',
      '.' + LOCKED_CLASS + ' .' + OPTION_CLASS + ':not(.' + SELECTED_CLASS + '):hover > .' + BTN_CLASS + ',',
      '.' + LOCKED_CLASS + ' .' + OPTION_CLASS + ':not(.' + SELECTED_CLASS + '):focus-within > .' + BTN_CLASS + ' {',
      '  opacity: 0;',
      '  transform: scale(0.85);',
      '  pointer-events: none;',
      '}',
      '.' + LOCKED_CLASS + ' .' + BTN_CLASS + ' {',
      '  cursor: default;',
      '}',
      '.' + FLASH_CLASS + ' {',
      '  outline: 2px solid var(--mc-accent, #0062CC);',
      '  outline-offset: 2px;',
      '  box-shadow: 0 0 0 4px var(--mc-accent-subtle, rgba(0, 98, 204, 0.122));',
      '  transition: outline-color var(--mc-dur-base, 200ms) cubic-bezier(0.22, 1, 0.36, 1), box-shadow var(--mc-dur-base, 200ms) cubic-bezier(0.22, 1, 0.36, 1);',
      '}',
      '@media (prefers-reduced-motion: reduce) {',
      '  .' + OPTION_CLASS + ', .' + BTN_CLASS + ', .' + FLASH_CLASS + ' {',
      '    transition-duration: 0ms !important;',
      '    animation-duration: 0ms !important;',
      '  }',
      '}',
      '.' + REDUCE_CLASS + ' .' + OPTION_CLASS + ',',
      '.' + REDUCE_CLASS + ' .' + BTN_CLASS + ',',
      '.' + REDUCE_CLASS + ' .' + FLASH_CLASS + ' {',
      '  transition-duration: 0ms !important;',
      '  animation-duration: 0ms !important;',
      '}'
    ].join('\n');

    var target = document.head || document.documentElement || document.body;
    if (target) {
      target.appendChild(style);
    }
  }

  function ensureRelativePosition(el) {
    var pos = '';
    if (typeof window.getComputedStyle === 'function') {
      try {
        pos = window.getComputedStyle(el).position;
      } catch (_) {
        pos = '';
      }
    }
    if (!pos && el.style) {
      pos = el.style.position;
    }
    if ((!pos || pos === 'static') && el.style) {
      el.style.position = 'relative';
    }
  }

  function mountControls() {
    for (var i = 0; i < groups.length; i++) {
      (function (group) {
        for (var j = 0; j < group.options.length; j++) {
          (function (opt) {
            ensureRelativePosition(opt.el);
            opt.el.classList.add(OPTION_CLASS);

            var btn = document.createElement('button');
            btn.type = 'button';
            btn.className = BTN_CLASS;
            btn.setAttribute('aria-label', '选这版 ' + opt.option);
            btn.addEventListener('click', function (e) {
              if (e && typeof e.stopPropagation === 'function') {
                e.stopPropagation();
              }
              select(group.question, opt.option);
            });

            opt.el.appendChild(btn);
            opt.btn = btn;
          })(group.options[j]);
        }
      })(groups[i]);
    }
  }

  function syncDOM() {
    if (typeof document !== 'undefined' && document.documentElement && document.documentElement.classList) {
      document.documentElement.classList.toggle(LOCKED_CLASS, locked);
    }
    for (var i = 0; i < groups.length; i++) {
      var g = groups[i];
      for (var j = 0; j < g.options.length; j++) {
        var opt = g.options[j];
        var selected = opt.option === g.selected;
        opt.el.classList.toggle(SELECTED_CLASS, selected);
        opt.el.classList.toggle(DIMMED_CLASS, !selected);
        if (opt.btn) {
          opt.btn.setAttribute('aria-pressed', selected ? 'true' : 'false');
          opt.btn.innerHTML = selected ? CHECK_SVG : '';
          opt.btn.disabled = locked;
          if (locked) {
            opt.btn.setAttribute('disabled', '');
          } else if (typeof opt.btn.removeAttribute === 'function') {
            opt.btn.removeAttribute('disabled');
          }
        }
      }
    }
  }

  function findGroup(question) {
    for (var i = 0; i < groups.length; i++) {
      if (groups[i].question === question) {
        return groups[i];
      }
    }
    return null;
  }

  function getState() {
    var changed = 0;
    var choices = [];
    for (var i = 0; i < groups.length; i++) {
      var g = groups[i];
      if (g.selected !== g.defaultOption) {
        changed += 1;
      }
      var optNames = [];
      var currentOpt = null;
      for (var j = 0; j < g.options.length; j++) {
        var o = g.options[j];
        optNames.push(o.option);
        if (o.option === g.selected) {
          currentOpt = o;
        }
      }
      choices.push({
        question: g.question,
        option: g.selected,
        recommended: !!(currentOpt && currentOpt.isRecommended),
        options: optNames
      });
    }
    return {
      version: version,
      locked: locked,
      changed: changed,
      choices: choices
    };
  }

  function postState() {
    if (!isInIframe()) return;
    if (typeof window.parent.postMessage === 'function') {
      window.parent.postMessage({
        source: MSG_SOURCE,
        type: 'state',
        state: getState()
      }, '*');
    }
  }

  function select(question, option) {
    if (locked) return;
    var group = findGroup(question);
    if (!group) return;
    var exists = false;
    for (var i = 0; i < group.options.length; i++) {
      if (group.options[i].option === option) {
        exists = true;
        break;
      }
    }
    if (!exists || group.selected === option) {
      return;
    }
    group.selected = option;
    version += 1;
    syncDOM();
    postState();
  }

  function scrollTo(question) {
    var group = findGroup(question);
    if (!group) return;
    if (typeof group.el.scrollIntoView === 'function') {
      group.el.scrollIntoView({
        block: 'center',
        behavior: isReduceMotion() ? 'auto' : 'smooth'
      });
    }
    if (group.flashTimer !== null) {
      clearTimeout(group.flashTimer);
      group.flashTimer = null;
    }
    group.el.classList.add(FLASH_CLASS);
    group.flashTimer = setTimeout(function () {
      group.el.classList.remove(FLASH_CLASS);
      group.flashTimer = null;
    }, FLASH_DURATION_MS);
  }

  function setReduceMotion(value) {
    reduceMotionOverride = !!value;
    if (typeof document !== 'undefined' && document.documentElement && document.documentElement.classList) {
      document.documentElement.classList.toggle(REDUCE_CLASS, reduceMotionOverride);
    }
  }

  function setLocked(value) {
    var next = !!value;
    if (next === locked) {
      return;
    }
    locked = next;
    version += 1;
    syncDOM();
    postState();
  }

  function onMessage(event) {
    if (!isInIframe()) return;
    if (!event || event.source !== window.parent) return;
    var data = event.data;
    if (!data || typeof data !== 'object' || data.source !== MSG_SOURCE) return;

    if (data.type === 'select') {
      select(data.question, data.option);
    } else if (data.type === 'scrollTo') {
      scrollTo(data.question);
    } else if (data.type === 'reduceMotion') {
      setReduceMotion(!!data.value);
    } else if (data.type === 'lock') {
      setLocked(!!data.value);
    } else if (data.type === 'getState') {
      postState();
    }
  }

  function init() {
    if (initialized) return;
    initialized = true;
    collectGroups();
    if (groups.length > 0) {
      injectStyle();
      mountControls();
      syncDOM();
    }
    if (isInIframe() && typeof window.addEventListener === 'function') {
      window.addEventListener('message', onMessage);
      postState();
    }
  }

  window.__mcodePick = {
    state: getState,
    select: select,
    scrollTo: scrollTo,
    setReduceMotion: setReduceMotion,
    setLocked: setLocked
  };

  if (typeof document !== 'undefined' && document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
