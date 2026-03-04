import React, { useState, useEffect, useRef } from 'react';
import { useDoc } from '@docusaurus/plugin-content-docs/client';
import { useLocation } from '@docusaurus/router';
import BrowserOnly from '@docusaurus/BrowserOnly';
import styles from './styles.module.css';

function CopyPageButtonContent(): JSX.Element {
  const [isOpen, setIsOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const doc = useDoc();
  const location = useLocation();

  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    }

    document.addEventListener('mousedown', handleClickOutside);
    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
    };
  }, []);

  const getMarkdownContent = (): string => {
    try {
      const article = document.querySelector('article');
      if (!article) {
        return `# ${doc.metadata.title}\n\n${doc.metadata.description || ''}`;
      }

      let markdown = `# ${doc.metadata.title}\n\n`;

      if (doc.metadata.description) {
        markdown += `${doc.metadata.description}\n\n`;
      }

      const clonedArticle = article.cloneNode(true) as HTMLElement;

      clonedArticle.querySelectorAll('nav, .theme-doc-version-badge, .theme-doc-breadcrumbs, .pagination-nav, .theme-doc-footer').forEach(el => el.remove());

      clonedArticle.querySelectorAll('h1, h2, h3, h4, h5, h6').forEach(heading => {
        const level = parseInt(heading.tagName[1]);
        const prefix = '#'.repeat(level);
        const text = heading.textContent?.trim() || '';
        heading.replaceWith(document.createTextNode(`\n${prefix} ${text}\n\n`));
      });

      clonedArticle.querySelectorAll('pre code').forEach(code => {
        const language = code.className.match(/language-(\w+)/)?.[1] || '';
        const text = code.textContent || '';
        const pre = code.closest('pre');
        if (pre) {
          pre.replaceWith(document.createTextNode(`\n\`\`\`${language}\n${text}\n\`\`\`\n\n`));
        }
      });

      clonedArticle.querySelectorAll('code:not(pre code)').forEach(code => {
        const text = code.textContent || '';
        code.replaceWith(document.createTextNode(`\`${text}\``));
      });

      clonedArticle.querySelectorAll('a').forEach(link => {
        const text = link.textContent || '';
        const href = link.getAttribute('href') || '';
        link.replaceWith(document.createTextNode(`[${text}](${href})`));
      });

      clonedArticle.querySelectorAll('li').forEach(li => {
        const text = li.textContent?.trim() || '';
        li.replaceWith(document.createTextNode(`- ${text}\n`));
      });

      clonedArticle.querySelectorAll('p').forEach(p => {
        const text = p.textContent?.trim() || '';
        if (text) {
          p.replaceWith(document.createTextNode(`${text}\n\n`));
        }
      });

      markdown += clonedArticle.textContent?.trim() || '';
      markdown = markdown.replace(/\n{3,}/g, '\n\n');

      return markdown;
    } catch (error) {
      console.error('Failed to extract markdown:', error);
      return `# ${doc.metadata.title}\n\n${doc.metadata.description || ''}\n\nSource: ${location.pathname}`;
    }
  };

  const handleCopyPage = async () => {
    const markdown = getMarkdownContent();
    await navigator.clipboard.writeText(markdown);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
    setIsOpen(false);
  };

  const handleOpenInChatGPT = () => {
    const fullUrl = window.location.href;
    const prompt = `Read from ${fullUrl} so I can ask questions about it.`;
    const encodedPrompt = encodeURIComponent(prompt);
    window.open(`https://chat.openai.com/?q=${encodedPrompt}`, '_blank');
    setIsOpen(false);
  };

  const handleOpenInClaude = async () => {
    const fullUrl = window.location.href;
    const prompt = `Read from ${fullUrl} so I can ask questions about it.`;
    await navigator.clipboard.writeText(prompt);
    window.open('https://claude.ai/new', '_blank');
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
    setIsOpen(false);
  };

  return (
    <div className={styles.copyPageContainer} ref={dropdownRef}>
      <button
        className={styles.copyPageButton}
        onClick={() => setIsOpen(!isOpen)}
        aria-label="Copy page options"
      >
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
          <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
        </svg>
        <span>Copy page</span>
        <svg
          width="12"
          height="12"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          className={styles.chevron}
        >
          <polyline points="6 9 12 15 18 9"></polyline>
        </svg>
      </button>

      {isOpen && (
        <div className={styles.dropdown}>
          <button className={styles.dropdownItem} onClick={handleCopyPage}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
              <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
            </svg>
            <div className={styles.itemContent}>
              <div className={styles.itemTitle}>Copy page</div>
              <div className={styles.itemDescription}>Copy page as Markdown for LLMs</div>
            </div>
          </button>

          <button className={styles.dropdownItem} onClick={handleOpenInChatGPT}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <circle cx="12" cy="12" r="10"></circle>
              <path d="M12 16v-4"></path>
              <path d="M12 8h.01"></path>
            </svg>
            <div className={styles.itemContent}>
              <div className={styles.itemTitle}>
                Open in ChatGPT
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{ marginLeft: '4px' }}>
                  <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path>
                  <polyline points="15 3 21 3 21 9"></polyline>
                  <line x1="10" y1="14" x2="21" y2="3"></line>
                </svg>
              </div>
              <div className={styles.itemDescription}>Ask questions about this page</div>
            </div>
          </button>

          <button className={styles.dropdownItem} onClick={handleOpenInClaude}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"></path>
            </svg>
            <div className={styles.itemContent}>
              <div className={styles.itemTitle}>
                Open in Claude
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{ marginLeft: '4px' }}>
                  <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path>
                  <polyline points="15 3 21 3 21 9"></polyline>
                  <line x1="10" y1="14" x2="21" y2="3"></line>
                </svg>
              </div>
              <div className={styles.itemDescription}>Ask questions about this page</div>
            </div>
          </button>
        </div>
      )}

      {copied && (
        <div className={styles.copiedNotification}>
          ✓ Copied to clipboard
        </div>
      )}
    </div>
  );
}

export default function CopyPageButton(): JSX.Element {
  return (
    <BrowserOnly fallback={<div />}>
      {() => <CopyPageButtonContent />}
    </BrowserOnly>
  );
}
