(function () {
  "use strict";

  var form = document.getElementById("import-form");
  var submitButton = document.getElementById("submit-button");
  var errorBox = document.getElementById("form-error");
  var emptyResult = document.getElementById("empty-result");
  var snapshotResult = document.getElementById("snapshot-result");
  var tokenSection = document.getElementById("token-section");
  var existingNote = document.getElementById("existing-note");
  var steps = Array.prototype.slice.call(document.querySelectorAll("[data-step]"));
  var timezone = document.getElementById("timezone");

  try {
    timezone.value = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch (error) {
    timezone.value = "UTC";
  }

  function setProgress(activeIndex) {
    steps.forEach(function (step, index) {
      step.removeAttribute("data-state");
      if (index < activeIndex) {
        step.setAttribute("data-state", "done");
      } else if (index === activeIndex) {
        step.setAttribute("data-state", "active");
      }
    });
  }

  function validateShareURL(rawURL) {
    var parsed;
    try {
      parsed = new URL(rawURL);
    } catch (error) {
      throw new Error("Enter a valid URL.");
    }
    var host = parsed.hostname.toLowerCase();
    var allowedHost = host === "chatgpt.com" || host === "www.chatgpt.com" ||
      host === "chat.openai.com" || host === "www.chat.openai.com";
    if (parsed.protocol !== "https:" || !allowedHost || !/^\/share\/[^/]+\/?$/.test(parsed.pathname)) {
      throw new Error("Use a public ChatGPT Share URL from chatgpt.com or chat.openai.com.");
    }
  }

  function displayError(message) {
    errorBox.textContent = message;
    errorBox.hidden = false;
    setProgress(-1);
  }

  function setOutput(id, value) {
    document.getElementById(id).value = value || "";
  }

  function showResult(result) {
    emptyResult.hidden = true;
    snapshotResult.hidden = false;
    existingNote.hidden = !result.existing;
    tokenSection.hidden = !result.admin_token;

    document.getElementById("result-badge").textContent = result.existing ? "Existing" : "Published";
    document.getElementById("result-summary").textContent = result.existing ? "No new revision was created" : "Immutable revision created";
    document.getElementById("result-name").textContent = result.title || "Untitled conversation";
    document.getElementById("result-revision").textContent = result.revision || "";
    document.getElementById("result-messages").textContent = String(result.message_count || 0);
    setOutput("page-url", result.page_url);
    setOutput("embed-url", result.embed_url);
    document.getElementById("page-open").href = result.page_url || "#";
    document.getElementById("embed-open").href = result.embed_url || "#";
    document.getElementById("admin-token").textContent = result.admin_token || "";
  }

  async function copyText(value, button) {
    if (!value) {
      return;
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(value);
    } else {
      var textarea = document.createElement("textarea");
      textarea.value = value;
      textarea.style.position = "fixed";
      textarea.style.opacity = "0";
      document.body.appendChild(textarea);
      textarea.select();
      document.execCommand("copy");
      textarea.remove();
    }
    var original = button.textContent;
    button.textContent = "Copied";
    window.setTimeout(function () {
      button.textContent = original;
    }, 1400);
  }

  document.addEventListener("click", function (event) {
    var button = event.target.closest("[data-copy-target]");
    if (!button) {
      return;
    }
    var target = document.getElementById(button.getAttribute("data-copy-target"));
    var value = target.value !== undefined ? target.value : target.textContent;
    copyText(value, button).catch(function () {
      displayError("Clipboard access was blocked. Select and copy the value manually.");
    });
  });

  form.addEventListener("submit", async function (event) {
    event.preventDefault();
    errorBox.hidden = true;
    var shareURL = document.getElementById("share-url").value.trim();

    try {
      setProgress(0);
      validateShareURL(shareURL);
    } catch (error) {
      displayError(error.message);
      document.getElementById("share-url").focus();
      return;
    }

    var timeoutValue = Number(document.getElementById("timeout").value || 0);
    if (!Number.isInteger(timeoutValue) || timeoutValue < 0 || timeoutValue > 300) {
      displayError("Timeout must be a whole number from 0 to 300 seconds.");
      return;
    }

    var payload = {
      url: shareURL,
      title: document.getElementById("snapshot-title").value.trim(),
      include_hidden: document.getElementById("include-hidden").checked,
      all_nodes: document.getElementById("all-nodes").checked,
      timezone: timezone.value.trim() || "UTC",
      timeout_seconds: timeoutValue
    };

    submitButton.disabled = true;
    submitButton.textContent = "Creating snapshot...";
    setProgress(1);

    try {
      var response = await fetch("/api/v1/snapshots", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
      var result = await response.json();
      if (!response.ok) {
        var message = result && result.error && result.error.message;
        throw new Error(message || "The snapshot could not be created.");
      }
      setProgress(3);
      showResult(result);
    } catch (error) {
      displayError(error.message || "The snapshot could not be created.");
    } finally {
      submitButton.disabled = false;
      submitButton.textContent = "Create snapshot";
    }
  });
})();
