import type { APIRoute, GetStaticPaths } from 'astro';
import { examples, highlight } from '../../examples';

export const getStaticPaths: GetStaticPaths = () => examples.map((example) => ({
  params: { target: example.id }, props: { example },
}));

export const GET: APIRoute = async ({ props }) => {
  const example = props.example as typeof examples[number];
  return Response.json({ ...example, html: await highlight(example.code, example.lang) });
};
