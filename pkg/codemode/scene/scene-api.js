// The scene builder a program writes against. It only records a node tree;
// nothing is drawn here. Every node remembers the program line that made it
// so the validator can point at it.
var __scene = (function () {
  var LIMIT = (typeof __sceneMaxNodes === "number") ? __sceneMaxNodes : 5000;
  var roots = [];
  var materials = {};
  var stack = [];
  var count = 0;
  var lineRe = /program\.js:(\d+)/;

  function callerLine() {
    var m = lineRe.exec(String(new Error().stack));
    return m ? Number(m[1]) : 0;
  }
  function fail(what, msg) { throw new Error(what + ": " + msg); }
  function num(what, field, v, positive) {
    if (typeof v !== "number" || !isFinite(v)) fail(what, field + " must be a finite number, got " + JSON.stringify(v));
    if (positive && !(v > 0)) fail(what, field + " must be greater than 0, got " + v);
    return v;
  }
  function vec(what, field, v, dflt) {
    if (v === undefined || v === null) return [dflt, dflt, dflt];
    if (typeof v === "number") return [num(what, field, v), num(what, field, v), num(what, field, v)];
    if (Array.isArray(v)) {
      if (v.length !== 3) fail(what, field + " must have 3 components, got " + v.length);
      return [num(what, field + "[0]", v[0]), num(what, field + "[1]", v[1]), num(what, field + "[2]", v[2])];
    }
    if (typeof v === "object") {
      var keys = Object.keys(v);
      for (var i = 0; i < keys.length; i++) {
        if (keys[i] !== "x" && keys[i] !== "y" && keys[i] !== "z") fail(what, field + " has unknown key '" + keys[i] + "' (use x, y, z)");
      }
      return [
        v.x === undefined ? dflt : num(what, field + ".x", v.x),
        v.y === undefined ? dflt : num(what, field + ".y", v.y),
        v.z === undefined ? dflt : num(what, field + ".z", v.z)
      ];
    }
    fail(what, field + " must be {x,y,z} or [x,y,z], got " + JSON.stringify(v));
  }
  function color(what, c) {
    if (typeof c === "number" && isFinite(c) && c >= 0 && c <= 0xffffff) {
      var s = Math.floor(c).toString(16);
      while (s.length < 6) s = "0" + s;
      return "#" + s;
    }
    if (typeof c === "string" && /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(c)) return c.toLowerCase();
    fail(what, "color must be '#rrggbb', '#rgb' or 0xRRGGBB, got " + JSON.stringify(c));
  }
  function checkKeys(what, obj, allowed) {
    var keys = Object.keys(obj);
    for (var i = 0; i < keys.length; i++) {
      if (allowed.indexOf(keys[i]) < 0) fail(what, "unknown option '" + keys[i] + "' (allowed: " + allowed.join(", ") + ")");
    }
  }
  function clamp01(what, f, v) {
    num(what, f, v);
    if (v < 0 || v > 1) fail(what, f + " must be between 0 and 1, got " + v);
    return v;
  }
  function materialSpec(what, m) {
    if (typeof m !== "object" || m === null) fail(what, "material must be an object");
    checkKeys(what, m, ["color", "metalness", "roughness", "opacity", "emissive"]);
    var out = { color: m.color === undefined ? "#b0b4bb" : color(what, m.color) };
    if (m.metalness !== undefined) out.metalness = clamp01(what, "metalness", m.metalness);
    if (m.roughness !== undefined) out.roughness = clamp01(what, "roughness", m.roughness);
    if (m.opacity !== undefined) out.opacity = clamp01(what, "opacity", m.opacity);
    if (m.emissive !== undefined) out.emissive = color(what, m.emissive);
    return out;
  }
  function add(node) {
    count++;
    if (count > LIMIT) throw new Error("node limit reached: a scene may have at most " + LIMIT + " nodes");
    node.line = callerLine();
    if (stack.length) stack[stack.length - 1].children.push(node);
    else roots.push(node);
    return { name: node.name, type: node.type };
  }
  function mesh(type, what, geometry, pos, opts) {
    if (opts === undefined || opts === null) opts = {};
    if (typeof opts !== "object") fail(what, "options must be an object");
    checkKeys(what, opts, ["name", "color", "material", "rotation", "metalness", "roughness", "opacity"]);
    var node = { type: type, name: opts.name === undefined ? "" : String(opts.name), geometry: geometry,
      position: vec(what, "position", pos, 0) };
    var rot = vec(what, "rotation", opts.rotation, 0);
    if (rot[0] || rot[1] || rot[2]) node.rotation = rot;
    if (opts.material !== undefined) {
      if (opts.color !== undefined) fail(what, "give either material or color, not both");
      if (typeof opts.material !== "string" || !materials[opts.material]) fail(what, "unknown material " + JSON.stringify(opts.material) + "; define it first with scene.material(name, {...})");
      node.material = opts.material;
    } else if (opts.color !== undefined || opts.metalness !== undefined || opts.roughness !== undefined || opts.opacity !== undefined) {
      node.material = materialSpec(what, { color: opts.color, metalness: opts.metalness, roughness: opts.roughness, opacity: opts.opacity });
    }
    return add(node);
  }
  function geom(what, size, allowed) {
    if (typeof size !== "object" || size === null || Array.isArray(size)) fail(what, "size must be an object like {" + allowed.join(", ") + "}");
    checkKeys(what, size, allowed);
    return size;
  }

  var api = {
    box: function (size, position, opts) {
      var s = geom("scene.box", size, ["w", "h", "d"]);
      return mesh("box", "scene.box", { w: num("scene.box", "w", s.w, true), h: num("scene.box", "h", s.h, true), d: num("scene.box", "d", s.d, true) }, position, opts);
    },
    cylinder: function (size, position, opts) {
      var s = geom("scene.cylinder", size, ["r", "rTop", "rBottom", "h", "segments"]);
      var rTop = s.rTop !== undefined ? s.rTop : s.r;
      var rBottom = s.rBottom !== undefined ? s.rBottom : s.r;
      var g = { rTop: num("scene.cylinder", "rTop (or r)", rTop), rBottom: num("scene.cylinder", "rBottom (or r)", rBottom), h: num("scene.cylinder", "h", s.h, true) };
      if (g.rTop < 0 || g.rBottom < 0 || (g.rTop === 0 && g.rBottom === 0)) fail("scene.cylinder", "radii must be >= 0 and not both 0");
      if (s.segments !== undefined) {
        num("scene.cylinder", "segments", s.segments, true);
        g.segments = Math.max(3, Math.min(128, Math.round(s.segments)));
      }
      return mesh("cylinder", "scene.cylinder", g, position, opts);
    },
    sphere: function (size, position, opts) {
      var s = geom("scene.sphere", size, ["r"]);
      return mesh("sphere", "scene.sphere", { r: num("scene.sphere", "r", s.r, true) }, position, opts);
    },
    group: function (name, transform, fn) {
      if (typeof transform === "function" && fn === undefined) { fn = transform; transform = {}; }
      if (typeof fn !== "function") fail("scene.group", "the last argument must be a function that adds the group's children");
      if (transform === undefined || transform === null) transform = {};
      if (typeof transform !== "object") fail("scene.group", "transform must be an object like {position, rotation, scale}");
      checkKeys("scene.group", transform, ["position", "rotation", "scale"]);
      var node = { type: "group", name: String(name === undefined ? "" : name), position: vec("scene.group", "position", transform.position, 0), children: [] };
      var rot = vec("scene.group", "rotation", transform.rotation, 0);
      if (rot[0] || rot[1] || rot[2]) node.rotation = rot;
      if (transform.scale !== undefined) {
        var sc = vec("scene.group", "scale", transform.scale, 1);
        if (!(sc[0] > 0 && sc[1] > 0 && sc[2] > 0)) fail("scene.group", "scale components must be greater than 0");
        node.scale = sc;
      }
      var handle = add(node);
      stack.push(node);
      try { fn(); } finally { stack.pop(); }
      return handle;
    },
    material: function (name, spec) {
      if (typeof name !== "string" || !name) fail("scene.material", "name must be a non-empty string");
      materials[name] = materialSpec("scene.material", spec || {});
      return name;
    },
    count: function () { return count; }
  };

  return {
    api: api,
    export: function () {
      return { version: 1, units: "m", up: "y", materials: materials, nodes: roots };
    }
  };
})();
// Name the methods that do exist when a program calls one that does not.
var scene = new Proxy(__scene.api, {
  get: function (target, prop) {
    if (typeof prop === "string" && !(prop in target) &&
        ["toJSON", "then", "constructor", "valueOf", "toString", "inspect"].indexOf(prop) < 0) {
      throw new Error("scene." + prop + " does not exist; the scene API has " + Object.keys(target).join(", "));
    }
    return target[prop];
  }
});
