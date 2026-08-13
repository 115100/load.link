var gallery = document.getElementById("gallery");

if (gallery) {
  var gallery_total = document.getElementById("gallery_total");
  var gallery_pages = document.getElementById("gallery_pages");
  var gallery_search = document.getElementById("gallery_search");
  var gallery_count = 0;
  var gallery_offset = 0;
  var gallery_loading = false;
  var gallery_done = false;
  var gallery_cur_page = 1;
  var gallery_query = "";
  var search_timer = null;
  var registered = [];

  // Read initial page and search query from URL
  (function () {
    var params = new URLSearchParams(window.location.search);
    var q = params.get("q") || "";
    gallery_query = q;
    if (gallery_search) gallery_search.value = q;
    var p = parseInt(params.get("page"), 10) || 1;
    if (p > 1) {
      gallery_cur_page = p;
      gallery_offset = (p - 1) * gallery_limit;
    }
  })();

  function api_call(action, params, cb) {
    params.action = action;
    var d = new FormData();
    d.append(
      "headers",
      new Blob([JSON.stringify(params)], { type: "application/json" }),
    );
    var x = new XMLHttpRequest();
    x.open("POST", api, true);
    x.onload = function () {
      if (x.status === 200) cb(JSON.parse(x.response));
    };
    x.send(d);
  }

  // Build the URL for the current page and search state.
  function gallery_url(page) {
    var parts = [];
    if (gallery_query)
      parts.push("q=" + encodeURIComponent(gallery_query));
    if (page > 1) parts.push("page=" + page);
    return parts.length ? "?" + parts.join("&") : "";
  }

  function esc_html(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return {
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;",
      }[c];
    });
  }

  function load_more() {
    if (gallery_loading || gallery_done) return;
    gallery_loading = true;

    var params = { limit: gallery_limit, offset: gallery_offset };
    var query = gallery_query;
    if (query) params.search = query;

    api_call("get_links", params, function (resp) {
      if (query !== gallery_query) return; // stale response, ignore
      gallery_loading = false;

      if (!resp.links || !resp.links.length) {
        gallery_done = true;
        if (gallery_offset === 0) {
          gallery.innerHTML =
            '<div class="gallery-empty">No items match &ldquo;' +
            esc_html(gallery_query) +
            '&rdquo;.</div>';
        }
        return;
      }

      gallery_cur_page = Math.floor(gallery_offset / gallery_limit) + 1;
      var latest = resp.links[0].date ? resp.links[0].date.slice(0, 10) : "";
      gallery.insertAdjacentHTML(
        "beforeend",
        '<div class="gallery-page-marker">Page ' +
          gallery_cur_page +
          " &mdash; " +
          latest +
          "</div>",
      );

      resp.links.forEach(function (item) {
        var el = document.createElement("div");
        el.className = "gallery item";
        el.setAttribute("data-uid", item.uid);
        el.setAttribute("data-name", item.name);
        el.setAttribute("data-mime", item.mime);
        el.setAttribute("data-ext", item.ext);
        gallery.appendChild(el);
        build_item(el, item);
      });
      gallery_offset += resp.links.length;
      if (gallery_offset >= gallery_count) gallery_done = true;
      render_pages();
    });
  }

  function load_page(page) {
    gallery_cur_page = page;
    gallery_offset = (page - 1) * gallery_limit;
    gallery.innerHTML = "";
    gallery_done = false;
    gallery_loading = false;
    registered = [];
    render_pages();
    load_more();
    window.scrollTo(0, 0);
  }

  function go_to_page(page) {
    if (page === gallery_cur_page) return;
    load_page(page);
    history.pushState(
      { page: page, q: gallery_query },
      "",
      gallery_url(page),
    );
  }

  // Commit a new search term: reset to page 1 and reload.
  function set_search(q) {
    q = q || "";
    if (q === gallery_query) return;
    gallery_query = q;
    load_page(1);
    history.pushState(
      { page: 1, q: gallery_query },
      "",
      gallery_url(1),
    );
  }

  // Search input: debounce keystrokes, commit immediately on Enter.
  if (gallery_search) {
    gallery_search.addEventListener("input", function () {
      var q = gallery_search.value;
      clearTimeout(search_timer);
      search_timer = setTimeout(function () {
        set_search(q);
      }, 300);
    });
    gallery_search.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        clearTimeout(search_timer);
        set_search(gallery_search.value);
      }
    });
  }

  function render_pages() {
    if (gallery_count <= gallery_limit) {
      gallery_pages.style.display = "none";
      return;
    }
    gallery_pages.style.display = "block";
    var total = Math.ceil(gallery_count / gallery_limit);
    var cur = gallery_cur_page;
    var h = "";
    if (cur > 1)
      h += '<a href="#" data-page="' + (cur - 1) + '">&laquo; Prev</a> ';
    h +=
      'Page <input type="text" inputmode="numeric" min="1" max="' +
      total +
      '" value="' +
      cur +
      '"> of ' +
      total;
    if (cur < total)
      h += ' <a href="#" data-page="' + (cur + 1) + '">Next &raquo;</a>';
    gallery_pages.innerHTML = h;
  }

  function build_item(el, item) {
    if (registered.indexOf(item.uid) > -1) return;
    registered.push(item.uid);

    // Delete button
    var cb = document.createElement("div");
    cb.className = "gallery closebutton";
    cb.addEventListener("click", function (e) {
      e.preventDefault();
      if (deletion_confirmation && !confirm('Delete "' + item.name + '"?'))
        return;
      api_call("delete", { uid: item.uid }, function () {
        el.style.display = "none";
        api_call(
          "count",
          gallery_query ? { search: gallery_query } : {},
          function (resp) {
            gallery_total.innerHTML = gallery_count = resp.count;
            render_pages();
          },
        );
      });
    });
    el.appendChild(cb);

    // Thumbnail or icon
    if (["image/jpeg", "image/png", "image/gif"].indexOf(item.mime) >= 0) {
      api_call(
        "get_thumbnail",
        { uid: item.uid },
        function (resp) {
          el.insertAdjacentHTML("beforeend", item_html(item, resp));
        },
      );
    } else {
      el.insertAdjacentHTML("beforeend", item_html(item));
    }
  }

  function item_html(item, thumb) {
    var src, w, h;
    if (thumb) {
      src = "data:" + thumb.mime + ";base64," + thumb.data;
      w = thumb.width;
      h = thumb.height;
    } else {
      var icon = item.mime.replace("/", "-") + ".svg";
      if (delfticons.indexOf(icon) < 0)
        icon =
          (item.mime.indexOf("video/") === 0 ? "video-x-generic" : "none") +
          ".svg";
      src = baseroute + "static/delfticons/" + icon;
      w = h = 96;
    }
    return (
      '<div class="thumbnail"><a href="' +
      baseroute +
      item.uid +
      (show_extension ? "." + item.ext : "") +
      '" title="' +
      item.name +
      " (" +
      item.mime +
      ')" target="_blank">' +
      '<img width="' +
      w +
      '" height="' +
      h +
      '" src="' +
      src +
      '" alt="' +
      item.mime +
      '"></a></div>'
    );
  }

  // Bootstrap
  api_call(
    "count",
    gallery_query ? { search: gallery_query } : {},
    function (resp) {
      gallery_total.innerHTML = gallery_count = resp.count;
      render_pages();
      load_more();
    },
  );

  // Back/forward navigation
  window.addEventListener("popstate", function (e) {
    var state = e.state || {};
    var q = state.q || "";
    var page = state.page || 1;
    if (q !== gallery_query) {
      gallery_query = q;
      if (gallery_search) gallery_search.value = q;
    }
    if (page !== gallery_cur_page) load_page(page);
  });

  // Infinite scroll
  window.addEventListener("scroll", function () {
    if (!gallery_loading && !gallery_done) {
      if (
        window.innerHeight + window.scrollY >=
        document.documentElement.offsetHeight - 400
      ) {
        load_more();
      }
    }
  });

  // Pagination controls (delegated)
  gallery_pages.addEventListener("click", function (e) {
    e.preventDefault();
    var page = parseInt(e.target.getAttribute("data-page"), 10);
    if (page) go_to_page(page);
  });
  gallery_pages.addEventListener("keydown", function (e) {
    if (e.key === "Enter") {
      var total = Math.ceil(gallery_count / gallery_limit);
      var p = parseInt(e.target.value, 10);
      if (p >= 1 && p <= total) go_to_page(p);
    }
  });

  // Scroll-to-top link
  document.addEventListener("click", function (e) {
    if (e.target.classList.contains("js-scroll-top")) {
      e.preventDefault();
      window.scroll(0, 0);
    }
  });
}
