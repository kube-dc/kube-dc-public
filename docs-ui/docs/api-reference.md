---
title: API Reference
description: Complete API documentation
---

# API Reference

Complete reference for the documentation website components and utilities.

## Components

### DocPage

The main component for rendering markdown documentation.

**Props:**

| Prop | Type | Description |
|------|------|-------------|
| `docPath` | `string` | Path to the markdown file (without .md extension) |

**Usage:**

```jsx
<DocPage docPath="getting-started" />
```

### AppHeader

The application header with navigation toggle.

**Props:**

| Prop | Type | Description |
|------|------|-------------|
| `onSidebarToggle` | `function` | Callback for sidebar toggle |

### AppSidebar

The navigation sidebar component.

**Props:**

| Prop | Type | Description |
|------|------|-------------|
| `isOpen` | `boolean` | Whether the sidebar is open |

## Utilities

### loadMarkdownDoc

Loads and parses a markdown document.

**Signature:**

```javascript
loadMarkdownDoc(docPath: string): Promise<{
  metadata: object,
  content: string
}>
```

**Parameters:**

- `docPath`: Path to the markdown file (without extension)

**Returns:**

Promise resolving to an object with:
- `metadata`: Frontmatter data
- `content`: Markdown content

**Example:**

```javascript
const { content, metadata } = await loadMarkdownDoc('getting-started');
console.log(metadata.title); // "Getting Started"
```

### docStructure

Array defining the navigation structure.

**Type:**

```typescript
Array<{
  title: string;
  path: string;
}>
```

## Markdown Features

### Supported Syntax

- **Headers**: `# H1`, `## H2`, etc.
- **Emphasis**: `*italic*`, `**bold**`
- **Lists**: Ordered and unordered
- **Links**: `[text](url)`
- **Images**: `![alt](url)`
- **Code**: Inline and blocks
- **Tables**: GitHub Flavored Markdown tables
- **Blockquotes**: `> quote`

### GitHub Flavored Markdown

The site uses `remark-gfm` plugin for extended features:

- Task lists
- Strikethrough
- Tables
- Autolinks
