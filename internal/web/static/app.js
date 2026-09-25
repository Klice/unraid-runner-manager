(function () {
  function startLogs() {
    var pre = document.getElementById("log");
    if (!pre || !window.EventSource) return;
    var follow = document.getElementById("log-follow");
    var source = new EventSource(pre.dataset.source);
    var first = true;
    var maxLines = 5000;
    source.onmessage = function (ev) {
      if (first) {
        pre.textContent = "";
        first = false;
      }
      pre.appendChild(document.createTextNode(ev.data + "\n"));
      while (pre.childNodes.length > maxLines) pre.removeChild(pre.firstChild);
      if (!follow || follow.checked) pre.scrollTop = pre.scrollHeight;
    };
    source.addEventListener("end", function () {
      if (first) pre.textContent = "(no output yet)";
      pre.appendChild(document.createTextNode("\n--- container stopped producing output ---\n"));
      source.close();
    });
    source.onerror = function () {
      if (first) pre.textContent = "Log stream unavailable. Reload to retry.";
    };
    window.addEventListener("beforeunload", function () { source.close(); });
  }

  function wireDialogs() {
    document.addEventListener("click", function (ev) {
      var close = ev.target.closest("[data-close-dialog]");
      if (close && close.closest("#modal")) {
        ev.preventDefault();
        document.getElementById("modal").innerHTML = "";
      }
    });
    document.addEventListener("keydown", function (ev) {
      if (ev.key === "Escape") {
        var modal = document.getElementById("modal");
        if (modal) modal.innerHTML = "";
      }
    });
  }

  function wireConfirms() {
    document.addEventListener("submit", function (ev) {
      var form = ev.target.closest("form[data-confirm]");
      if (form && !window.confirm(form.dataset.confirm)) ev.preventDefault();
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    startLogs();
    wireDialogs();
    wireConfirms();
  });
})();
