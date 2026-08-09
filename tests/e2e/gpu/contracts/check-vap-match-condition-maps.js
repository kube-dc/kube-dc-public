#!/usr/bin/env node

// A ValidatingAdmissionPolicy matchCondition is a negative-selection boundary:
// an evaluation error under failurePolicy=Fail denies the request before the
// policy can skip an unrelated object. Every fixed-key lookup on an optional
// map must therefore prove both that the map exists and that the key exists.
// Validations are intentionally not checked here: malformed in-scope GPU
// objects are expected to fail closed there.

const fs = require('node:fs');

function indentation(line) {
  return line.length - line.trimStart().length;
}

function policyName(lines, before) {
  for (let index = before; index >= 0; index -= 1) {
    const match = /^\s{2}name:\s+(\S+)\s*$/.exec(lines[index]);
    if (match) return match[1];
    if (lines[index].trim() === '---') break;
  }
  return 'unknown-policy';
}

function matchConditionExpressions(rendered) {
  const lines = rendered.split(/\r?\n/);
  const expressions = [];
  for (let index = 0; index < lines.length; index += 1) {
    if (lines[index].trim() !== 'matchConditions:') continue;
    const sectionIndent = indentation(lines[index]);
    const name = policyName(lines, index);
    index += 1;
    while (index < lines.length) {
      const line = lines[index];
      if (line.trim() && indentation(line) <= sectionIndent) {
        index -= 1;
        break;
      }
      const match = /^\s*expression:\s*(.*)$/.exec(line);
      if (!match) {
        index += 1;
        continue;
      }
      const expressionIndent = indentation(line);
      const parts = [];
      const inline = match[1].trim();
      if (inline && !['>-', '|-', '>', '|'].includes(inline)) parts.push(inline);
      index += 1;
      while (index < lines.length) {
        const continuation = lines[index];
        if (continuation.trim() && indentation(continuation) <= expressionIndent) break;
        if (continuation.trim()) parts.push(continuation.trim());
        index += 1;
      }
      expressions.push({ policy: name, expression: parts.join(' ') });
    }
  }
  return expressions;
}

function unsafeLookups(rendered) {
  const issues = [];
  for (const { policy, expression } of matchConditionExpressions(rendered)) {
    const lookups = [];
    const bracketLookup = /((?:object|oldObject|request)(?:\.[A-Za-z_][A-Za-z0-9_]*)+)\[['"]([^'"]+)['"]\]/g;
    for (const match of expression.matchAll(bracketLookup)) {
      lookups.push({ map: match[1], key: match[2] });
    }
    const dotMapLookup = /((?:object|oldObject)(?:\.[A-Za-z_][A-Za-z0-9_]*)*\.(?:labels|annotations))\.([A-Za-z_][A-Za-z0-9_]*)/g;
    for (const match of expression.matchAll(dotMapLookup)) {
      lookups.push({ map: match[1], key: match[2] });
    }
    for (const { map, key } of lookups) {
      const hasMap = expression.includes(`has(${map})`);
      const hasKey = expression.includes(`'${key}' in ${map}`)
        || expression.includes(`"${key}" in ${map}`);
      if (!hasMap || !hasKey) {
        issues.push({ policy, map, key, hasMap, hasKey, expression });
      }
    }
  }
  return issues;
}

function selfTest() {
  const unsafe = `
kind: ValidatingAdmissionPolicy
metadata:
  name: unsafe
spec:
  matchConditions:
    - name: select
      expression: >-
        has(object.metadata.labels) &&
        object.metadata.labels['example.com/key'] == 'value'
  validations:
    - expression: "true"
`;
  const safe = unsafe.replace(
    "has(object.metadata.labels) &&\n        object.metadata.labels",
    "has(object.metadata.labels) &&\n        'example.com/key' in object.metadata.labels &&\n        object.metadata.labels",
  );
  const unsafeDot = unsafe.replace(
    "object.metadata.labels['example.com/key']",
    'object.metadata.labels.example',
  );
  if (unsafeLookups(unsafe).length !== 1
      || unsafeLookups(safe).length !== 0
      || unsafeLookups(unsafeDot).length !== 1) {
    throw new Error('VAP optional-map guard checker self-test failed');
  }
}

function main() {
  selfTest();
  const file = process.argv[2];
  if (!file) {
    console.error('usage: check-vap-match-condition-maps.js <rendered-chart.yaml>');
    process.exit(2);
  }
  const issues = unsafeLookups(fs.readFileSync(file, 'utf8'));
  if (issues.length === 0) return;
  for (const issue of issues) {
    console.error(
      `${issue.policy}: unsafe matchCondition lookup ${issue.map}['${issue.key}'] `
      + `(map guard=${issue.hasMap}, key guard=${issue.hasKey})`,
    );
  }
  process.exit(1);
}

main();
