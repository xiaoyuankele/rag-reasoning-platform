<template>
  <div class="public-auth-layout">
    <header class="auth-brand">
      <RouterLink to="/login" aria-label="返回登录页">
        <span class="auth-brand-mark" aria-hidden="true">R</span>
        <span>
          <strong>RAG Workspace</strong>
          <small>个人研究工作台</small>
        </span>
      </RouterLink>
    </header>

    <main class="public-auth-main">
      <RouterView />
    </main>

    <footer>内容优先，来源可追溯。</footer>
  </div>
</template>

<style scoped>
.public-auth-layout {
  display: grid;
  min-height: 100vh;
  grid-template-rows: auto 1fr auto;
  padding: 24px clamp(20px, 5vw, 64px);
  background:
    radial-gradient(circle at 50% -20%, var(--color-accent-soft), transparent 38%),
    var(--color-surface-subtle);
}

.auth-brand a {
  display: inline-flex;
  align-items: center;
  gap: 11px;
  color: var(--color-text-strong);
  text-decoration: none;
}

.auth-brand-mark {
  display: grid;
  width: 34px;
  height: 34px;
  place-items: center;
  border: 1px solid var(--color-border-strong);
  border-radius: 10px;
  background: var(--color-surface);
  font-weight: 700;
}

.auth-brand strong,
.auth-brand small {
  display: block;
}

.auth-brand strong {
  font-size: 14px;
}

.auth-brand small,
footer {
  color: var(--color-text-subtle);
  font-size: 12px;
}

.auth-brand small {
  margin-top: 2px;
}

.public-auth-main {
  display: grid;
  width: 100%;
  place-items: center;
  padding: 48px 0;
}

footer {
  text-align: center;
}
</style>
