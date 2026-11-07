import { readFileSync, writeFileSync } from 'node:fs';

const schema = JSON.parse(readFileSync(new URL('../../docs/remote-control.schema.json', import.meta.url), 'utf8'));

function typeOf(node) {
  if (node.oneOf || node.anyOf) return (node.oneOf ?? node.anyOf).map(typeOf).join(' | ');
  if ('$ref' in node) {
    const name = node.$ref.replace('#/$defs/', '');
    if (!schema.$defs[name]) throw new Error(`Unknown schema reference: ${node.$ref}`);
    return name;
  }
  if ('const' in node) return JSON.stringify(node.const);
  if (node.enum) return node.enum.map(JSON.stringify).join(' | ');
  if (Array.isArray(node.type)) return node.type.map(type => typeOf({ ...node, type })).join(' | ');
  switch (node.type) {
    case 'string': return 'string';
    case 'integer':
    case 'number': return 'number';
    case 'boolean': return 'boolean';
    case 'null': return 'null';
    case 'array': return `Array<${typeOf(node.items ?? {})}>`;
    case 'object': {
      const properties = Object.entries(node.properties ?? {});
      if (!properties.length) {
        if (node.additionalProperties && typeof node.additionalProperties === 'object') {
          return `Record<string, ${typeOf(node.additionalProperties)}>`;
        }
        return node.additionalProperties === false ? 'Record<string, never>' : 'Record<string, unknown>';
      }
      return `{\n${properties.map(([name, value]) => `  ${name}${node.required?.includes(name) ? '' : '?'}: ${typeOf(value)};`).join('\n')}\n}`;
    }
    case undefined: return 'unknown';
    default: throw new Error(`Unsupported schema type: ${node.type}`);
  }
}

const output = '// Generated from docs/remote-control.schema.json; run npm run generate:contract.\n\n' +
  Object.entries(schema.$defs).map(([name, node]) => `export type ${name} = ${typeOf(node)};`).join('\n\n') + '\n';
writeFileSync(new URL('../src/types/generated.ts', import.meta.url), output);
