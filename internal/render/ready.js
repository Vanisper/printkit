(async () => {
  const images = new Set();
  const inspectSVG = url => {
    const comma = url.indexOf(',');
    const metadata = url.slice(0, comma);
    let source;
    try {
      const encoded = decodeURIComponent(url.slice(comma + 1));
      source = /;base64$/i.test(metadata)
        ? new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(atob(encoded), c => c.charCodeAt(0)))
        : encoded;
    } catch { throw new Error('SVG 图片编码无效'); }
    const svg = new DOMParser().parseFromString(source, 'image/svg+xml');
    if (svg.querySelector('parsererror')) throw new Error('SVG 图片语法无效');
    for (const node of svg.querySelectorAll('*')) {
      if (/^(script|style|image|feimage|foreignobject|animate|animatemotion|animatetransform|set)$/i.test(node.localName)) {
        throw new Error('data SVG 仅支持静态矢量内容，嵌套资源或样式请转为 PNG 或内联 SVG');
      }
      for (const attr of node.attributes) {
        if (/^on/i.test(attr.name) || (/^(href|src)$/i.test(attr.localName) && !attr.value.startsWith('#'))) {
          throw new Error('data SVG 不支持脚本或嵌套资源');
        }
        // fill、filter 等 SVG 展示属性也可以包含资源引用
        const presentation = document.createElement('span').style;
        presentation.setProperty(attr.localName, attr.value);
        checkStyle(presentation, false, true);
      }
      if (node.style) checkStyle(node.style, false, true);
    }
  };
  const checkURL = (url, image) => {
    url = url.trim();
    if (url.startsWith('#')) return;
    if (!/^data:/i.test(url)) throw new Error('资源仅允许 data URL，禁止外部网络或本地文件');
    if (image) {
      if (images.has(url)) return;
      if (!/^data:image\//i.test(url)) throw new Error('图片 data URL 必须声明 image 类型');
      if (/^data:image\/svg\+xml[;,]/i.test(url)) inspectSVG(url);
      images.add(url);
    }
  };
  const checkStyle = (style, font = false, embeddedSVG = false) => {
    for (const name of style) {
      const value = style.getPropertyValue(name);
      // CSSOM 将合法的 URL 值序列化为 url(...)，保留字体与图片的加载类型
      const urls = value.matchAll(/url\(\s*(?:"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'|([^)]*))\s*\)/gi);
      for (const match of urls) {
        const url = (match[1] ?? match[2] ?? match[3]).replace(/\\([0-9a-f]{1,6})\s?|\\(.)/gi,
          (_, hex, char) => hex ? String.fromCodePoint(parseInt(hex, 16)) : char);
        if (embeddedSVG && !url.startsWith('#')) throw new Error('data SVG 不支持嵌套资源');
        checkURL(url, !font);
      }
    }
  };
  const checkRules = rules => {
    for (const rule of rules) {
      if (rule.type === CSSRule.IMPORT_RULE) throw new Error('不支持 CSS @import，请将样式内联');
      if (rule.style) checkStyle(rule.style, rule.type === CSSRule.FONT_FACE_RULE);
      if (rule.cssRules) checkRules(rule.cssRules);
    }
  };
  for (const sheet of document.styleSheets) checkRules(sheet.cssRules);
  for (const node of document.querySelectorAll('*')) {
    if (node.style) checkStyle(node.style);
    // 覆盖 CSS 变量和 image-set 等由浏览器解析后才确定的图片地址
    for (const pseudo of [null, '::before', '::after']) checkStyle(getComputedStyle(node, pseudo));
  }
  await Promise.all([...document.fonts].map(font => font.load().catch(() => {
    throw new Error('字体加载失败：' + font.family);
  })));
  await document.fonts.ready;
  await Promise.all([...document.images].map(async (img, index) => {
    if (!img.getAttribute('src')) throw new Error('图片缺少 src：第 ' + (index + 1) + ' 张');
    checkURL(img.currentSrc || img.src, true);
    img.loading = 'eager';
    try { await img.decode(); } catch { throw new Error('图片解码失败：第 ' + (index + 1) + ' 张'); }
    if (!img.naturalWidth || !img.naturalHeight) throw new Error('图片尺寸无效');
  }));
  for (const image of document.querySelectorAll('svg image, svg feImage')) {
    checkURL(image.getAttribute('href') || image.getAttribute('xlink:href') || '', true);
  }
  for (const image of document.querySelectorAll('input[type="image"]')) {
    checkURL(image.getAttribute('src') || '', true);
  }
  await Promise.all([...images].map(async src => {
    const image = new Image();
    image.src = src;
    try { await image.decode(); } catch { throw new Error('CSS 或 SVG 图片解码失败'); }
  }));
  // 让字体与图片引起的排版更新在生成 PDF 前完成
  await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  return true;
})()
