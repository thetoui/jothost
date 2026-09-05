import { LinkButton } from '@/components/ui/Link';

export function NotFoundPage() {
  return (
    <div className="mx-auto max-w-md py-16 text-center">
      <h1 className="text-2xl font-semibold text-slate-900">Page not found</h1>
      <p className="mt-2 text-sm text-slate-600">
        That page does not exist in this build of the panel.
      </p>
      <LinkButton to="/" variant="primary" className="mt-6">
        Back to dashboard
      </LinkButton>
    </div>
  );
}
