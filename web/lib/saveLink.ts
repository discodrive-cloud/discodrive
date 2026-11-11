// saveLink makes the browser download url under name: the browser writes the file to
// disk itself, with its own progress, pause and resume, and the page holds none of it.
export function saveLink(url: string, name: string, doc: Document = document) {
  const a = doc.createElement('a')
  a.href = url
  a.download = name
  a.rel = 'noopener'
  a.style.display = 'none'
  doc.body.appendChild(a)
  a.click()
  a.remove()
}
