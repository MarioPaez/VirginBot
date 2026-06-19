package calendar

import (
	"strings"

	"golang.org/x/net/html"
)

// attrVal devuelve el valor de un atributo, o "" si no existe.
func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// hasClass indica si el nodo tiene la clase CSS indicada.
func hasClass(n *html.Node, cls string) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, f := range strings.Fields(attrVal(n, "class")) {
		if f == cls {
			return true
		}
	}
	return false
}

// findAll devuelve todos los nodos del árbol que cumplen pred (en preorden).
func findAll(root *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if pred(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// firstWithClass devuelve el primer descendiente con la clase CSS indicada.
func firstWithClass(root *html.Node, cls string) *html.Node {
	return first(root, func(n *html.Node) bool { return hasClass(n, cls) })
}

// firstTag devuelve el primer descendiente con la etiqueta indicada.
func firstTag(root *html.Node, tag string) *html.Node {
	return first(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == tag
	})
}

// first devuelve el primer descendiente (excluida la raíz) que cumple pred.
func first(root *html.Node, pred func(*html.Node) bool) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n != root && pred(n) {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found
}

// textContent devuelve el texto concatenado de un nodo y sus descendientes,
// con los espacios colapsados.
func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(sb.String()), " ")
}
