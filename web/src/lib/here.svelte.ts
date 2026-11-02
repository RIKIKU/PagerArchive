// The current page's own name, for the last breadcrumb (R79) when it comes
// from data (an alias's name, a user's), set by the page once it's loaded.
// It counts only for the address it was set on.
export const here = $state({ path: '', title: '' });

export function setTitle(title: string) {
	here.path = location.pathname;
	here.title = title;
}
