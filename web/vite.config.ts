import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, type Plugin } from 'vitest/config';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';

// `virtual:lucide` is the map of exactly the Lucide icons the source mentions
// (`lucide:name` string literals), name -> SVG body. The whole set is ~1500
// icons; shipping all of it to draw forty of them was the single largest
// chunk in the app. Scanning at build time keeps "write the name where you use
// it" as the only step -- there is no list to forget to update.
function lucideSubset(): Plugin {
	const id = 'virtual:lucide';
	const resolved = '\0' + id;
	const srcDir = join(import.meta.dirname, 'src');

	function sourceFiles(dir: string): string[] {
		return readdirSync(dir).flatMap((name) => {
			const p = join(dir, name);
			if (statSync(p).isDirectory()) return sourceFiles(p);
			return /\.(svelte|ts)$/.test(name) && !name.endsWith('.spec.ts') ? [p] : [];
		});
	}

	function build(): string {
		const used = new Set<string>();
		for (const file of sourceFiles(srcDir)) {
			for (const m of readFileSync(file, 'utf8').matchAll(/lucide:([a-z0-9-]+)/g)) used.add(m[1]);
		}
		const set = JSON.parse(
			readFileSync(
				join(import.meta.dirname, 'node_modules/@iconify-json/lucide/icons.json'),
				'utf8'
			)
		) as { icons: Record<string, { body: string }>; aliases?: Record<string, { parent: string }> };
		const out: Record<string, string> = {};
		for (const name of [...used].sort()) {
			const body = (set.icons[name] ?? set.icons[set.aliases?.[name]?.parent ?? ''])?.body;
			if (body === undefined) throw new Error(`unknown Lucide icon "lucide:${name}"`);
			out[name] = body;
		}
		return `export default ${JSON.stringify(out)};`;
	}

	return {
		name: 'lucide-subset',
		resolveId: (source) => (source === id ? resolved : undefined),
		load: (loaded) => (loaded === resolved ? build() : undefined),
		// A new icon name in a component must reach the map without restarting dev.
		handleHotUpdate({ file, server }) {
			if (!file.startsWith(srcDir) || !/\.(svelte|ts)$/.test(file)) return;
			const mod = server.moduleGraph.getModuleById(resolved);
			if (mod) {
				server.moduleGraph.invalidateModule(mod);
				server.ws.send({ type: 'full-reload' });
			}
		}
	};
}

// Where `pnpm dev` forwards API traffic. Overridable so a second instance
// (a throwaway database, say) can run beside the usual one.
const api = process.env.API_ORIGIN ?? 'http://localhost:8080';

export default defineConfig({
	plugins: [
		tailwindcss(),
		lucideSubset(),
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},

			// SPA build: single fallback HTML page served for all routes, client-side
			// routing takes over from there (see src/routes/+layout.ts for ssr: false).
			adapter: adapter({
				pages: 'build',
				assets: 'build',
				fallback: 'index.html',
				precompress: false,
				strict: true
			})
		})
	],
	server: {
		port: Number(process.env.PORT ?? 5173),
		proxy: {
			// forwards API calls (and rendered file pages + the MCP endpoint) to
			// the Go backend during `pnpm dev`
			'/api': api,
			'/res': api,
			'/mcp': api
		}
	},
	test: {
		expect: { requireAssertions: true },
		environment: 'node',
		include: ['src/**/*.{test,spec}.{js,ts}']
	}
});
