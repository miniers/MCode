import React from 'react';
import { CodeBlock } from './MarkdownView';
import { loadMermaid } from '../utils/lazyMermaid';

let renderSeq = 0;

export function readIsDarkTheme(): boolean {
  if (typeof document !== 'undefined') {
    return document.documentElement.classList.contains('dark');
  }
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
  }
  return false;
}

export function useIsDarkTheme(): boolean {
  const [isDark, setIsDark] = React.useState<boolean>(readIsDarkTheme);

  React.useEffect(() => {
    if (typeof document === 'undefined') return;
    const root = document.documentElement;
    setIsDark(root.classList.contains('dark'));

    if (typeof MutationObserver === 'undefined') return;
    const observer = new MutationObserver(() => {
      setIsDark(root.classList.contains('dark'));
    });
    observer.observe(root, { attributes: true, attributeFilter: ['class'] });
    return () => observer.disconnect();
  }, []);

  return isDark;
}

interface RenderedDiagram {
  svg: string;
  bindFunctions?: (element: Element) => void;
}

export function MermaidBlock({ content }: { content: string }) {
  const isDark = useIsDarkTheme();
  const [rendered, setRendered] = React.useState<RenderedDiagram | null>(null);
  const [failed, setFailed] = React.useState(false);
  const containerRef = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    let cancelled = false;
    setFailed(false);

    loadMermaid()
      .then(async (mermaid) => {
        if (cancelled) return;
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          suppressErrorRendering: true,
          theme: isDark ? 'dark' : 'default'
        });
        const id = `mcode-mermaid-${++renderSeq}`;
        const result = await mermaid.render(id, content);
        if (cancelled) return;
        setRendered({ svg: result.svg, bindFunctions: result.bindFunctions });
      })
      .catch(() => {
        if (cancelled) return;
        setRendered(null);
        setFailed(true);
      });

    return () => {
      cancelled = true;
    };
  }, [content, isDark]);

  React.useEffect(() => {
    const el = containerRef.current;
    if (!el || !rendered) return;
    const svgEl = el.querySelector('svg');
    if (svgEl) {
      svgEl.style.maxWidth = '100%';
    }
    rendered.bindFunctions?.(el);
  }, [rendered]);

  if (failed || rendered === null) {
    return <CodeBlock content={content} language="mermaid" />;
  }

  return (
    <div
      ref={containerRef}
      className="min-w-0 max-w-full overflow-x-auto rounded-xl border border-[var(--border)] bg-[var(--card)] p-3 [&_svg]:max-w-full [&_svg]:h-auto"
      dangerouslySetInnerHTML={{ __html: rendered.svg }}
    />
  );
}
