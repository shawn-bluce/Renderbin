// Flat-key message catalogs for the two supported locales. `en` is the source
// of truth for the key set; `zh` must mirror it exactly (enforced by the
// `satisfies Record<MessageKey, string>` below). Placeholders use `{name}`
// syntax and are filled in by `t()` (see ./index.svelte.ts).

export const en = {
	// Header
	'header.settings': 'Settings',
	'header.logout': 'Log out',

	// Overview / tabs
	'overview.title': 'Overview',
	'tab.files': 'Files',
	'tab.trashed': 'Trashed',
	'count.total': '{n} total',
	'count.public': '{n} public',
	'count.private': '{n} private',
	'count.inTrash': '{n} in trash',

	// Upload
	'upload.button': 'Upload',
	'upload.uploading': 'Uploading…',
	'upload.hint': 'or drag & drop .html, .md, or .txt files anywhere',
	'upload.dropOverlay': 'Drop files to upload',

	// New-file modal
	'create.button': 'New',
	'create.title': 'New file',
	'create.nameLabel': 'Title',
	'create.kindLabel': 'Format',
	'create.kindHint': "Chosen once — a file's format cannot be changed later.",
	'create.contentLabel': 'Content',
	'create.contentPlaceholder': 'Paste or type the content…',
	'create.submit': 'Create',
	'create.creating': 'Creating…',
	'kind.html': 'HTML',
	'kind.markdown': 'Markdown',
	'kind.txt': 'Plain text',

	// List controls
	'list.searchPlaceholder': 'Search files…',
	'list.sort': 'Sort',
	'list.visibility': 'Visibility',
	'search.scopeTitle': 'Searches your own files',
	'search.content': 'Search content',
	'search.contentTitle': 'Also search inside file contents',
	'search.loading': 'Searching…',
	'search.empty': 'No files match your search.',
	'error.search': 'Search failed',
	'visibility.all': 'All',
	'visibility.public': 'Public',
	'visibility.private': 'Private',
	'sort.recent': 'Recent',
	'sort.name': 'Name',
	'sort.success': 'Successes',
	'sort.failure': 'Failures',

	// File row
	'row.formatTitle': 'Format: {kind}',
	'row.updated': 'Updated {time}',
	'row.expires': 'Expires {datetime}',
	'row.expiresTitle': 'This link goes offline at {datetime}',
	'row.viewsLeft': '{n} views left',
	'row.viewsLeftTitle': 'Remaining views before this link auto-expires',
	'row.expiredTtl': 'Offline: expired',
	'row.expiredViews': 'Offline: views used up',
	'row.expiredTitle':
		'This link took itself offline on {datetime} and the file is now private. Setting a new expiry clears this note.',
	'row.copyLink': 'Copy access link',
	'row.successfulViews': 'Views by a signed-in user (session cookie)',
	'row.codeViews': 'Views without a cookie but with the correct access code',
	'row.failedViews': 'Blocked access attempts',
	'row.removeTag': 'Remove tag "{tag}"',
	'row.tagsPlaceholder': 'a,b,c',
	'row.editTags': 'Edit tags (comma-separated)',
	'row.tag': 'Tag',
	'row.restore': 'Restore',
	'row.deletePermanent': 'Delete permanently',
	'row.editTitle': 'Edit title',
	'row.public': 'Public',
	'row.private': 'Private',
	'row.toggleVisibility': 'Toggle {name} visibility',
	'row.downloadSource': 'Download source',
	'row.edit': 'Edit',
	'row.setExpiry': 'Set expiry',
	'row.refreshCode': 'Refresh access code',
	'row.delete': 'Delete',

	// Empty / loading states
	'trash.loading': 'Loading trash…',
	'trash.empty': 'Trash is empty.',
	'trash.emptyAll': 'Empty trash',
	'trash.emptyAllTitle': 'Permanently delete everything in the trash',
	'files.emptyNone': 'No files yet — upload one above.',
	'files.emptyFiltered': 'No files match your filters.',

	// Day-group labels
	'date.today': 'Today',
	'date.yesterday': 'Yesterday',

	// Edit modal
	'edit.title': 'Edit file',
	'edit.titleLabel': 'Title',
	'edit.slugLabel': 'Slug',
	'edit.codeLabel': 'Access code',
	'edit.toggleContent': 'Show or hide the content editor',
	'source.markdown': 'Markdown source',
	'source.txt': 'Text content',
	'source.html': 'HTML source',

	// Expiry modal
	'expiry.title': 'Link expiry',
	'expiry.note':
		'Setting a time or view limit makes the file public. The two options are mutually exclusive.',
	'expiry.unlimited': 'Unlimited',
	'expiry.timeLimit': 'Time limit',
	'expiry.viewLimit': 'View limit',
	'expiry.expiresAfter': 'Expires after',
	'expiry.maxViews': 'Max views (anonymous access)',
	'expiry.noExpiry': 'No expiry — this clears any existing limit.',
	'expiry.preview': 'Expires {datetime} once saved.',
	'ttl.hours': 'Hours',
	'ttl.days': 'Days',
	'ttl.weeks': 'Weeks',
	'ttl.months': 'Months',
	'ttl.years': 'Years',

	// Common
	'common.cancel': 'Cancel',
	'common.save': 'Save',
	'common.saving': 'Saving…',
	'common.close': 'Close',
	'common.done': 'Done',

	// Copy-to-clipboard fallback. navigator.clipboard only exists in a secure
	// context, so on a plain-HTTP LAN deployment the copy can genuinely fail
	// and the link has to be shown somewhere selectable instead.
	'copy.failed': 'Your browser blocked the copy. Select the link and copy it manually:',
	'copy.dismiss': 'Dismiss',

	// Storage
	'quota.used': '{used} of {quota} used',
	'row.sizeTitle': 'Stored size',

	// Errors / status
	'error.noSupported': 'No supported files found — accepts .html, .md, and .txt.',
	'error.oversizeOnly': '{n} files over {size} — skipped.',
	'note.skipped': 'skipped {n} unsupported files',
	'note.oversize': '{n} files over {size}',
	'note.failed': '{n} uploads failed',
	'error.loadTrash': 'Failed to load trash',
	'error.restore': 'Failed to restore',
	'error.loadContent': 'Failed to load content',
	'error.slug': "Slug must be 1-128 chars of letters, digits, '.', '_' or '-'.",
	'error.contentRequired': 'Content is required.',
	'error.contentTooLarge': 'Content exceeds {size}.',
	'error.save': 'Failed to save',
	'error.viewCount': 'View count must be a positive integer.',
	'error.ttlValue': 'Enter a whole number of at least 1, up to {years} years out.',
	'error.visibility': 'Failed to update visibility',
	'error.saveTags': 'Failed to save tags',
	'error.removeTag': 'Failed to remove tag',
	'error.refreshCode': 'Failed to refresh access code',
	'error.delete': 'Failed to delete',
	'error.rename': 'Failed to rename',
	'error.deletePermanent': 'Failed to permanently delete',
	'error.slugTaken': 'This slug is already taken — pick a different one.',
	'error.accessCode': "Access code must be 1-128 chars of letters, digits, '.', '_' or '-'.",
	'confirm.delete': 'Move "{name}" to the trash? You can restore it from the Trash tab.',
	'confirm.refreshCode':
		'Issue a new access code for "{name}"? Every link already shared for this file stops working, and the old code cannot be recovered.',
	'confirm.deletePermanent': 'Permanently delete "{name}"? This cannot be undone.',
	'confirm.emptyTrash': 'Permanently delete all {n} files in the trash? This cannot be undone.',
	'error.emptyTrash': 'Failed to empty the trash',
	'error.create': 'Failed to create the file',
	untitled: 'Untitled',

	// Login page
	'login.signIn': 'Sign in',
	'login.subtitle': 'Host your files, share the links.',
	'login.username': 'Username',
	'login.password': 'Password',
	'login.signingIn': 'Signing in…',
	'login.invalidCredentials': 'Invalid username or password',
	'login.accountDisabled': 'This account has been disabled — contact the administrator.',
	'login.usernameRequired': 'Username is required',
	'login.passwordRequired': 'Password is required',
	'login.noAccount': 'No account yet?',
	'login.register': 'Register',

	// Register page
	'register.title': 'Create account',
	'register.subtitle': 'Register to host and share your files.',
	'register.nickname': 'Nickname',
	'register.rePassword': 'Confirm password',
	'register.submit': 'Register',
	'register.submitting': 'Registering…',
	'register.usernameRequired': 'Username is required',
	'register.usernameInvalid': "Username must be 1-64 chars of letters, digits, '.', '_' or '-'",
	'register.nicknameRequired': 'Nickname is required',
	'register.nicknameTooLong': 'Nickname must be at most 64 characters',
	'register.passwordTooShort': 'Password must be at least 6 characters',
	'register.rePasswordRequired': 'Please confirm the password',
	'register.passwordMismatch': 'Passwords do not match',
	'register.usernameTaken': 'This username is already taken',
	'register.failed': 'Registration failed',
	'register.disabled': 'Registration is currently disabled.',
	'register.haveAccount': 'Already have an account?',
	'register.signIn': 'Sign in',

	// Welcome (first-run setup) page
	'welcome.title': 'Welcome',
	'welcome.subtitle': 'Create the first account — it becomes the super admin.',
	'welcome.options': 'Options',
	'welcome.submit': 'Create account',
	'welcome.submitting': 'Creating…',
	'welcome.failed': 'Setup failed',

	// Shared config toggles (welcome + settings)
	'config.allowRegistration': 'Allow registration',
	'config.allowRegistrationHint': 'Anyone who can reach this site can create an account.',
	'config.enableMcp': 'Enable MCP',
	'config.enableMcpHint':
		'Lets AI clients manage your files over MCP: each user gets an API key, used as a Bearer token against the /mcp endpoint.',
	'config.uploadDefaultPublic': 'New files start public',
	'config.uploadDefaultPublicHint':
		'Files created on the dashboard or over the API start public, viewable by anyone with the link and access code. When off, every new file starts private. MCP uploads always start private.',

	// Settings page
	'settings.title': 'Settings',
	'settings.back': 'Back to files',
	'settings.user': 'User',
	'settings.nickname': 'Nickname',
	'settings.nicknameUpdated': 'Nickname updated.',
	'settings.changePassword': 'Change password',
	'settings.currentPassword': 'Current password',
	'settings.currentPasswordRequired': 'Current password is required',
	'settings.newPassword': 'New password',
	'settings.passwordUpdated': 'Password updated.',
	'settings.registration': 'Registration',
	'settings.uploads': 'Uploads',
	'backup.title': 'Backup & restore',
	'backup.download': 'Download a backup',
	'backup.downloadHint':
		'A single SQLite file containing everything: all accounts and all of their files.',
	'backup.downloadButton': 'Download',
	'backup.restore': 'Restore from a backup',
	'backup.restoreHint':
		'Uploads a backup file and replaces the current database with it. A backup taken by an older version is upgraded as it is restored.',
	'backup.chooseFile': 'Choose a file…',
	'backup.restoreWarning':
		'This replaces every account and every file with the contents of that backup. Anything created since it was taken is lost, and you will be signed out.',
	'backup.restoreButton': 'Replace the database',
	'backup.restoring': 'Restoring…',
	'backup.restored': 'Restored {users} account(s) and {files} file(s). Reloading…',
	'error.restore.failed': 'The restore failed and nothing was changed.',
	'settings.ai': 'AI capability',
	'settings.adminOnly': 'Only the super admin can change this.',
	'settings.apiKey': 'API Key',
	'settings.apiKeyHint': 'MCP clients connect to {endpoint} with this key as a Bearer token.',
	'settings.setupPrompt': 'Agent setup prompt',
	'settings.setupPromptHint':
		'Send this to your AI agent and it will install the MCP server for you.',
	'settings.setupPromptText':
		'Please install and configure an MCP server for me:\n' +
		'- Name: renderbin\n' +
		'- Transport: Streamable HTTP\n' +
		'- Endpoint: {endpoint}\n' +
		'- Auth: HTTP header "Authorization: Bearer {apiKey}"\n' +
		'\n' +
		'If you are Claude Code, run:\n' +
		'claude mcp add --transport http renderbin {endpoint} --header "Authorization: Bearer {apiKey}"\n' +
		'\n' +
		'If your tool uses a JSON config (mcpServers), add:\n' +
		'{"mcpServers":{"renderbin":{"type":"http","url":"{endpoint}","headers":{"Authorization":"Bearer {apiKey}"}}}}\n' +
		'\n' +
		'Then verify the connection: listing tools should show upload_file, upload_files, list_files, search_files, update_file, publish_file, unpublish_file and delete_file.',
	'settings.copy': 'Copy',
	'settings.copied': 'Copied',
	'settings.resetApiKey': 'Reset',
	'settings.resetConfirmTitle': 'Reset API Key?',
	'settings.resetConfirmBody':
		'The current key stops working immediately and every client using it must be updated. This cannot be undone.',
	'settings.resetConfirm': 'Reset key',
	'settings.resetting': 'Resetting…',
	'error.updateSettings': 'Failed to update settings',
	'error.wrongPassword': 'Current password is incorrect',
	'error.apiKey': 'Failed to load API key',

	// Accounts page (super admin only)
	'accounts.title': 'Accounts',
	'accounts.subtitle':
		'Every account on this instance. Files stay private to their owner — you only see how many there are.',
	'accounts.superAdmin': 'Super admin',
	'accounts.disabledBadge': 'Disabled',
	'accounts.files': '{n} files',
	'accounts.trashed': '{n} in trash',
	'accounts.created': 'Joined {date}',
	'accounts.disable': 'Disable',
	'accounts.enable': 'Enable',
	'accounts.disableTitle': "Block sign-in and take this account's links offline",
	'accounts.enableTitle': "Restore sign-in and bring this account's links back",
	'accounts.superAdminLocked':
		'The super admin cannot be disabled, and the role cannot be transferred.',
	'accounts.resetPassword': 'Reset password',
	'accounts.resetTitle': 'Reset password',
	'accounts.resetBody':
		'Set a new password for {name}. It applies immediately and signs out every device currently using the account. You do not need the old password.',
	'accounts.newPassword': 'New password',
	'accounts.resetDone': 'Password updated for {name}.',
	'accounts.disabledNote': "Sign-in is blocked and every link to this account's files returns 404.",
	'accounts.create': 'Add account',
	'accounts.createTitle': 'Add an account',
	'accounts.createBody':
		'The password is generated and shown once, here. Pass it on and have them change it from their own profile.',
	'accounts.username': 'Username',
	'accounts.nickname': 'Display name',
	'accounts.usernameRequired': 'A username is required.',
	'accounts.createdBody': 'Created {name}. This is the only time the password is shown:',
	'accounts.createdWarning':
		'It is not stored anywhere in readable form. Once you close this, the only way to give the account a known password is to reset it.',
	'accounts.copyPassword': 'Copy password',
	'accounts.deleteTitle': 'Delete account',
	'accounts.deleteConfirm': 'Delete permanently',
	'accounts.deleteBody': 'Delete {name} and all {n} of their files?',
	'accounts.deleteWarning':
		'The files are erased outright, not moved to a trash, and every link to them stops working. There is no undo. To block access reversibly, disable the account instead.',
	'accounts.deleteDone': 'Deleted {name} and {n} files.',
	'accounts.quotaEdit': 'Change the storage limit',
	'accounts.quotaLabel': 'Storage limit',
	'accounts.quotaUnitHint': 'e.g. 100MB, 2GB',
	'error.accountStatus': 'Failed to change the account status',
	'error.resetPassword': 'Failed to reset the password',
	'error.createAccount': 'Failed to create the account',
	'error.deleteAccount': 'Failed to delete the account',
	'error.quota': 'Failed to change the storage limit',
	'error.quotaInvalid': 'Enter a size, e.g. 100 or 500MB or 2GB.',
	'confirm.disableAccount':
		'Disable "{name}"? They cannot sign in and their shared links stop working until you re-enable them.',

	// Language switcher
	'switcher.label': 'Language'
} as const;

export type Locale = 'en' | 'zh';
export type MessageKey = keyof typeof en;

export const zh = {
	// Header
	'header.settings': '设置',
	'header.logout': '退出登录',

	// Overview / tabs
	'overview.title': '概览',
	'tab.files': '文件',
	'tab.trashed': '回收站',
	'count.total': '共 {n} 个',
	'count.public': '{n} 个公开',
	'count.private': '{n} 个私有',
	'count.inTrash': '回收站中 {n} 个',

	// Upload
	'upload.button': '上传',
	'upload.uploading': '上传中…',
	'upload.hint': '或将 .html、.md、.txt 文件拖放到任意位置',
	'upload.dropOverlay': '拖放文件以上传',

	// New-file modal
	'create.button': '新建',
	'create.title': '新建文件',
	'create.nameLabel': '标题',
	'create.kindLabel': '格式',
	'create.kindHint': '创建时确定，之后不可修改。',
	'create.contentLabel': '正文',
	'create.contentPlaceholder': '粘贴或输入内容…',
	'create.submit': '创建',
	'create.creating': '创建中…',
	'kind.html': 'HTML',
	'kind.markdown': 'Markdown',
	'kind.txt': '纯文本',

	// List controls
	'list.searchPlaceholder': '搜索文件…',
	'list.sort': '排序',
	'list.visibility': '可见性',
	'search.scopeTitle': '只搜索你自己的文件',
	'search.content': '搜索正文',
	'search.contentTitle': '同时搜索文件正文内容',
	'search.loading': '搜索中…',
	'search.empty': '没有匹配的文件。',
	'error.search': '搜索失败',
	'visibility.all': '全部',
	'visibility.public': '公开',
	'visibility.private': '私有',
	'sort.recent': '最近',
	'sort.name': '名称',
	'sort.success': '成功次数',
	'sort.failure': '失败次数',

	// File row
	'row.formatTitle': '格式：{kind}',
	'row.updated': '更新于 {time}',
	'row.expires': '到期 {datetime}',
	'row.expiresTitle': '该链接将于 {datetime} 下线',
	'row.viewsLeft': '剩余 {n} 次访问',
	'row.viewsLeftTitle': '此链接自动失效前的剩余访问次数',
	'row.expiredTtl': '已下线：超时',
	'row.expiredViews': '已下线：次数用尽',
	'row.expiredTitle':
		'该链接于 {datetime} 自动下线，文件已转为私有。重新设置有效期即可清除此标记。',
	'row.copyLink': '复制访问链接',
	'row.successfulViews': '已登录用户（会话 Cookie）的访问次数',
	'row.codeViews': '未带 Cookie 但访问码正确的访问次数',
	'row.failedViews': '被拦截的访问次数',
	'row.removeTag': '移除标签“{tag}”',
	'row.tagsPlaceholder': 'a,b,c',
	'row.editTags': '编辑标签（逗号分隔）',
	'row.tag': '标签',
	'row.restore': '恢复',
	'row.deletePermanent': '永久删除',
	'row.editTitle': '编辑标题',
	'row.public': '公开',
	'row.private': '私有',
	'row.toggleVisibility': '切换“{name}”的可见性',
	'row.downloadSource': '下载源文件',
	'row.edit': '编辑',
	'row.setExpiry': '设置有效期',
	'row.refreshCode': '刷新访问码',
	'row.delete': '删除',

	// Empty / loading states
	'trash.loading': '正在加载回收站…',
	'trash.empty': '回收站为空。',
	'trash.emptyAll': '清空回收站',
	'trash.emptyAllTitle': '永久删除回收站中的全部文件',
	'files.emptyNone': '还没有文件——在上方上传一个。',
	'files.emptyFiltered': '没有符合筛选条件的文件。',

	// Day-group labels
	'date.today': '今天',
	'date.yesterday': '昨天',

	// Edit modal
	'edit.title': '编辑文件',
	'edit.titleLabel': '标题',
	'edit.slugLabel': '链接标识',
	'edit.codeLabel': '访问码',
	'edit.toggleContent': '展开/收起内容编辑器',
	'source.markdown': 'Markdown 源码',
	'source.txt': '文本内容',
	'source.html': 'HTML 源码',

	// Expiry modal
	'expiry.title': '链接有效期',
	'expiry.note': '设置时间或访问次数限制会使文件变为公开。两个选项互斥。',
	'expiry.unlimited': '无限制',
	'expiry.timeLimit': '时间限制',
	'expiry.viewLimit': '访问次数限制',
	'expiry.expiresAfter': '有效期',
	'expiry.maxViews': '最大访问次数（匿名访问）',
	'expiry.noExpiry': '无有效期——这将清除任何现有限制。',
	'expiry.preview': '保存后将于 {datetime} 到期。',
	'ttl.hours': '小时',
	'ttl.days': '天',
	'ttl.weeks': '周',
	'ttl.months': '月',
	'ttl.years': '年',

	// Common
	'common.cancel': '取消',
	'common.save': '保存',
	'common.saving': '保存中…',
	'common.close': '关闭',
	'common.done': '完成',

	// Copy-to-clipboard fallback.
	'copy.failed': '浏览器阻止了复制，请手动选中并复制链接：',
	'copy.dismiss': '关闭',

	// Storage
	'quota.used': '已用 {used} / {quota}',
	'row.sizeTitle': '文件体积',

	// Errors / status
	'error.noSupported': '未找到受支持的文件——仅接受 .html、.md 和 .txt。',
	'error.oversizeOnly': '{n} 个文件超过 {size}——已跳过。',
	'note.skipped': '跳过 {n} 个不支持的文件',
	'note.oversize': '{n} 个文件超过 {size}',
	'note.failed': '{n} 个上传失败',
	'error.loadTrash': '加载回收站失败',
	'error.restore': '恢复失败',
	'error.loadContent': '加载内容失败',
	'error.slug': "链接标识必须为 1-128 个字符，仅含字母、数字、'.'、'_' 或 '-'。",
	'error.contentRequired': '内容不能为空。',
	'error.contentTooLarge': '内容超过 {size}。',
	'error.save': '保存失败',
	'error.viewCount': '访问次数必须为正整数。',
	'error.ttlValue': '请输入不小于 1 的整数，且到期时间不超过 {years} 年。',
	'error.visibility': '更新可见性失败',
	'error.saveTags': '保存标签失败',
	'error.removeTag': '移除标签失败',
	'error.refreshCode': '刷新访问码失败',
	'error.delete': '删除失败',
	'error.rename': '重命名失败',
	'error.deletePermanent': '永久删除失败',
	'error.slugTaken': '该链接标识已被占用，请换一个。',
	'error.accessCode': "访问码必须为 1-128 个字符，仅含字母、数字、'.'、'_' 或 '-'。",
	'confirm.delete': '将“{name}”移入回收站？可以在回收站标签页中恢复。',
	'confirm.refreshCode':
		'为“{name}”生成新的访问码？此文件已分享出去的所有链接都会立即失效，且旧访问码无法找回。',
	'confirm.deletePermanent': '永久删除“{name}”？此操作不可恢复。',
	'confirm.emptyTrash': '永久删除回收站中的全部 {n} 个文件？此操作不可恢复。',
	'error.emptyTrash': '清空回收站失败',
	'error.create': '创建文件失败',
	untitled: '未命名',

	// Login page
	'login.signIn': '登录',
	'login.subtitle': '托管你的文件，分享链接。',
	'login.username': '用户名',
	'login.password': '密码',
	'login.signingIn': '登录中…',
	'login.invalidCredentials': '用户名或密码错误',
	'login.accountDisabled': '该账号已被禁用，请联系管理员。',
	'login.usernameRequired': '请输入用户名',
	'login.passwordRequired': '请输入密码',
	'login.noAccount': '还没有账号？',
	'login.register': '注册',

	// Register page
	'register.title': '注册账号',
	'register.subtitle': '注册后即可托管和分享你的文件。',
	'register.nickname': '昵称',
	'register.rePassword': '确认密码',
	'register.submit': '注册',
	'register.submitting': '注册中…',
	'register.usernameRequired': '请输入用户名',
	'register.usernameInvalid': "用户名必须为 1-64 个字符，仅含字母、数字、'.'、'_' 或 '-'",
	'register.nicknameRequired': '请输入昵称',
	'register.nicknameTooLong': '昵称最多 64 个字符',
	'register.passwordTooShort': '密码至少 6 个字符',
	'register.rePasswordRequired': '请再次输入密码',
	'register.passwordMismatch': '两次输入的密码不一致',
	'register.usernameTaken': '该用户名已被占用',
	'register.failed': '注册失败',
	'register.disabled': '注册功能当前已关闭。',
	'register.haveAccount': '已有账号？',
	'register.signIn': '登录',

	// Welcome (first-run setup) page
	'welcome.title': '欢迎使用',
	'welcome.subtitle': '创建第一个账号——它将成为超级管理员。',
	'welcome.options': '选项',
	'welcome.submit': '创建账号',
	'welcome.submitting': '创建中…',
	'welcome.failed': '初始化失败',

	// Shared config toggles (welcome + settings)
	'config.allowRegistration': '允许注册',
	'config.allowRegistrationHint': '任何能访问本站的人都可以注册账号。',
	'config.enableMcp': '启用 MCP',
	'config.enableMcpHint':
		'允许 AI 客户端通过 MCP 管理你的文件：每个用户会获得一个 API Key，作为 Bearer Token 连接 /mcp 端点。',
	'config.uploadDefaultPublic': '新文件默认公开',
	'config.uploadDefaultPublicHint':
		'在仪表盘或通过 API 创建的文件默认公开，任何拿到链接和访问码的人都能立即查看；关闭时新文件默认私有。MCP 上传始终以私有创建。',

	// Settings page
	'settings.title': '设置',
	'settings.back': '返回文件列表',
	'settings.user': '用户',
	'settings.nickname': '昵称',
	'settings.nicknameUpdated': '昵称已更新。',
	'settings.changePassword': '修改密码',
	'settings.currentPassword': '当前密码',
	'settings.currentPasswordRequired': '请输入当前密码',
	'settings.newPassword': '新密码',
	'settings.passwordUpdated': '密码已更新。',
	'settings.registration': '注册',
	'settings.uploads': '上传',
	'backup.title': '备份与恢复',
	'backup.download': '下载备份',
	'backup.downloadHint': '一个 SQLite 文件，包含全部内容：所有账号及其所有文件。',
	'backup.downloadButton': '下载',
	'backup.restore': '从备份恢复',
	'backup.restoreHint':
		'上传一个备份文件，用它替换当前数据库。由旧版本导出的备份会在恢复过程中自动升级。',
	'backup.chooseFile': '选择文件…',
	'backup.restoreWarning':
		'该操作会用备份中的内容替换所有账号与所有文件。备份之后新增的数据都会丢失，你也会被登出。',
	'backup.restoreButton': '替换数据库',
	'backup.restoring': '恢复中…',
	'backup.restored': '已恢复 {users} 个账号、{files} 个文件。正在重新加载…',
	'error.restore.failed': '恢复失败，数据未发生任何变化。',
	'settings.ai': 'AI 能力',
	'settings.adminOnly': '仅超级管理员可修改。',
	'settings.apiKey': 'API Key',
	'settings.apiKeyHint': 'MCP 客户端以该 Key 作为 Bearer Token 连接 {endpoint}。',
	'settings.setupPrompt': 'Agent 安装提示词',
	'settings.setupPromptHint': '把这段话发给你的 AI Agent，它就会帮你完成 MCP 的安装配置。',
	'settings.setupPromptText':
		'请帮我安装配置一个 MCP 服务器，信息如下：\n' +
		'- 名称：renderbin\n' +
		'- 传输方式：Streamable HTTP\n' +
		'- 端点：{endpoint}\n' +
		'- 鉴权：HTTP Header「Authorization: Bearer {apiKey}」\n' +
		'\n' +
		'如果你是 Claude Code，可直接运行：\n' +
		'claude mcp add --transport http renderbin {endpoint} --header "Authorization: Bearer {apiKey}"\n' +
		'\n' +
		'如果你的工具使用 JSON 配置（mcpServers），请加入：\n' +
		'{"mcpServers":{"renderbin":{"type":"http","url":"{endpoint}","headers":{"Authorization":"Bearer {apiKey}"}}}}\n' +
		'\n' +
		'配置完成后请验证连接：列出工具时应能看到 upload_file、upload_files、list_files、search_files、update_file、publish_file、unpublish_file、delete_file 八个工具。',
	'settings.copy': '复制',
	'settings.copied': '已复制',
	'settings.resetApiKey': '重置',
	'settings.resetConfirmTitle': '重置 API Key？',
	'settings.resetConfirmBody':
		'当前 Key 将立即失效，所有使用它的客户端都需要更新。此操作不可撤销。',
	'settings.resetConfirm': '重置 Key',
	'settings.resetting': '重置中…',
	'error.updateSettings': '更新设置失败',
	'error.wrongPassword': '当前密码错误',
	'error.apiKey': '加载 API Key 失败',

	// Accounts page (super admin only)
	'accounts.title': '账号管理',
	'accounts.subtitle': '本站的所有账号。文件仍然只属于各自的属主，这里只显示数量。',
	'accounts.superAdmin': '超级管理员',
	'accounts.disabledBadge': '已禁用',
	'accounts.files': '{n} 个文件',
	'accounts.trashed': '回收站 {n} 个',
	'accounts.created': '{date} 加入',
	'accounts.disable': '禁用',
	'accounts.enable': '解禁',
	'accounts.disableTitle': '禁止登录，并让该账号的链接失效',
	'accounts.enableTitle': '恢复登录，并让该账号的链接重新可用',
	'accounts.superAdminLocked': '超级管理员不可禁用，身份也不可转移。',
	'accounts.resetPassword': '重置密码',
	'accounts.resetTitle': '重置密码',
	'accounts.resetBody':
		'为 {name} 设置新密码。立即生效，并会让该账号当前已登录的所有设备退出。无需知道旧密码。',
	'accounts.newPassword': '新密码',
	'accounts.resetDone': '已更新 {name} 的密码。',
	'accounts.disabledNote': '该账号无法登录，其名下文件的所有链接都会返回 404。',
	'accounts.create': '添加账号',
	'accounts.createTitle': '添加账号',
	'accounts.createBody':
		'密码由系统生成，且只在此处显示一次。请转交给对方，并让其在个人设置中自行修改。',
	'accounts.username': '用户名',
	'accounts.nickname': '昵称',
	'accounts.usernameRequired': '请填写用户名。',
	'accounts.createdBody': '已创建 {name}。密码仅在此显示这一次：',
	'accounts.createdWarning':
		'系统不会以明文保存该密码。关闭本窗口后，若要让该账号使用一个已知密码，只能重新重置。',
	'accounts.copyPassword': '复制密码',
	'accounts.deleteTitle': '删除账号',
	'accounts.deleteConfirm': '永久删除',
	'accounts.deleteBody': '删除 {name} 及其名下全部 {n} 个文件？',
	'accounts.deleteWarning':
		'这些文件会被直接抹除，不进回收站，所有指向它们的链接都会失效，且无法撤销。若只是想临时阻止访问，请改用禁用账号。',
	'accounts.deleteDone': '已删除 {name} 及其 {n} 个文件。',
	'accounts.quotaEdit': '修改存储上限',
	'accounts.quotaLabel': '存储上限',
	'accounts.quotaUnitHint': '例如 100MB、2GB',
	'error.accountStatus': '修改账号状态失败',
	'error.resetPassword': '重置密码失败',
	'error.createAccount': '创建账号失败',
	'error.deleteAccount': '删除账号失败',
	'error.quota': '修改存储上限失败',
	'error.quotaInvalid': '请输入一个体积，例如 100、500MB 或 2GB。',
	'confirm.disableAccount': '禁用“{name}”？该账号将无法登录，其分享链接在解禁前都会失效。',

	// Language switcher
	'switcher.label': '语言'
} as const satisfies Record<MessageKey, string>;

export const dictionaries: Record<Locale, Record<MessageKey, string>> = { en, zh };
