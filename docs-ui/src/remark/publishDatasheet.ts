type MarkdownNode = {
  type: string;
  depth?: number;
  value?: string;
  children?: MarkdownNode[];
};

type MarkdownRoot = {
  children: MarkdownNode[];
};

function textContent(node: MarkdownNode): string {
  if (typeof node.value === 'string') return node.value;
  return (node.children ?? []).map(textContent).join('');
}

/**
 * Keep review metadata in the Markdown source while omitting it from the
 * customer-facing datasheet website and its rendered PDF.
 */
export default function publishDatasheet() {
  return (tree: MarkdownRoot): void => {
    const draftHeading = tree.children.findIndex(
      (node) => node.type === 'heading'
        && node.depth === 1
        && /^DRAFT\b/.test(textContent(node).trim()),
    );

    if (draftHeading >= 0) {
      tree.children.splice(draftHeading, 1);
      while (
        tree.children[draftHeading]
        && ['blockquote', 'thematicBreak'].includes(tree.children[draftHeading].type)
      ) {
        tree.children.splice(draftHeading, 1);
      }
    }

    tree.children = tree.children.filter(
      (node) => node.type !== 'paragraph'
        || !/^📷\s/.test(textContent(node).trim()),
    );

    const apparatusHeading = tree.children.findIndex(
      (node) => node.type === 'heading'
        && node.depth === 2
        && /^Draft apparatus\b/.test(textContent(node).trim()),
    );

    if (apparatusHeading >= 0) {
      tree.children.splice(apparatusHeading);
    }
  };
}
