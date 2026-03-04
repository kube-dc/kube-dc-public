---
title: Configuration
description: Configure your documentation site
---

# Configuration

Learn how to configure and customize your documentation website.

## Navigation Structure

The navigation menu is defined in `src/utils/docStructure.js`:

```javascript
export const docStructure = [
  {
    title: 'Getting Started',
    path: 'getting-started'
  },
  {
    title: 'Installation',
    path: 'installation'
  }
];
```

### Adding Navigation Items

To add a new item to the navigation:

1. Add an entry to the `docStructure` array
2. Create the corresponding `.md` file in `public/docs/`

## Markdown Frontmatter

Each markdown file can include frontmatter metadata:

```yaml
---
title: Page Title
description: Page description
author: Your Name
date: 2024-01-01
---
```

### Available Frontmatter Fields

- **title**: Page title (displayed as H1)
- **description**: Page description
- **author**: Content author
- **date**: Publication date
- Custom fields as needed

## Styling

The documentation uses PatternFly v5 for styling. You can customize:

### Markdown Styles

Edit `src/components/DocPage.css` to customize markdown rendering:

```css
.markdown-content h1 {
  font-size: 2rem;
  color: #151515;
}
```

### PatternFly Theme

PatternFly styles are imported in `src/index.js`:

```javascript
import '@patternfly/react-core/dist/styles/base.css';
```

## Advanced Configuration

### Custom Components

You can extend the markdown renderer to support custom React components by modifying the `ReactMarkdown` configuration in `DocPage.js`.

### Routing

Routes are configured in `src/App.js` using React Router v6:

```javascript
<Routes>
  <Route path="/" element={<DocPage docPath="getting-started" />} />
  <Route path="/docs/:docPath" element={<DocPage />} />
</Routes>
```
