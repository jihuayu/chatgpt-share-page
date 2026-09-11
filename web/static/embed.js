(function () {
  "use strict";
  function postHeight() {
    if (window.parent && window.parent !== window) {
      window.parent.postMessage(
        { type: "chatgpt-share-page:height", height: document.documentElement.scrollHeight },
        "*"
      );
    }
  }
  window.addEventListener("load", postHeight);
  if (typeof ResizeObserver === "function") {
    new ResizeObserver(postHeight).observe(document.documentElement);
  }
  postHeight();
})();
