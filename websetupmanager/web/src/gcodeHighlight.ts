export interface GCodeToken {
  text: string
  kind?: 'comment' | 'command' | 'axis' | 'feed' | 'parameter' | 'number'
}

const tokenPattern = /(\([^\r\n)]*\)|;[^\r\n]*|\b[GM]\s*\d+(?:\.\d+)?|#[0-9]+|\b[XYZABCUVWIJKR]\s*[+-]?(?:\d+(?:\.\d*)?|\.\d+)|\b[FS]\s*[+-]?(?:\d+(?:\.\d*)?|\.\d+)|[+-]?(?:\d+(?:\.\d*)?|\.\d+))/gi

export function tokenizeGCode(value: string): GCodeToken[] {
  const result: GCodeToken[] = []
  let offset = 0
  for (const match of value.matchAll(tokenPattern)) {
    const index = match.index
    if (index > offset) result.push({ text: value.slice(offset, index) })
    const text = match[0]
    let kind: GCodeToken['kind'] = 'number'
    if (text.startsWith('(') || text.startsWith(';')) kind = 'comment'
    else if (/^[GM]/i.test(text)) kind = 'command'
    else if (/^[XYZABCUVWIJKR]/i.test(text)) kind = 'axis'
    else if (/^[FS]/i.test(text)) kind = 'feed'
    else if (text.startsWith('#')) kind = 'parameter'
    result.push({ text, kind })
    offset = index + text.length
  }
  if (offset < value.length) result.push({ text: value.slice(offset) })
  return result
}
