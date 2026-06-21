function refresh() {
  var here = window.location;
  window.location = here.protocol + "//" + here.host + here.pathname;
}

document.addEventListener("click", function (e) {
  if (e.target.classList.contains("js-refresh")) {
    e.preventDefault();
    refresh();
  }
});
